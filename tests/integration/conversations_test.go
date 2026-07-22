//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const conversationIntegrationKey = "sk-conversation-integration"

type conversationHTTPResult struct {
	status int
	body   []byte
	err    error
}

func TestConversationsComplexStateTransitions_PostgreSQL(t *testing.T) {
	testConversationsComplexStateTransitions(t, "postgresql")
}

func TestConversationsComplexStateTransitions_MongoDB(t *testing.T) {
	testConversationsComplexStateTransitions(t, "mongodb")
}

func testConversationsComplexStateTransitions(t *testing.T, dbType string) {
	t.Helper()
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:    dbType,
		MasterKey: conversationIntegrationKey,
	})
	t.Cleanup(func() { fixture.Shutdown(t) })

	created := conversationJSONRequest(t, fixture, http.MethodPost, "/v1/conversations", map[string]any{
		"metadata": map[string]string{"suite": "complex-" + dbType, "unicode": "Zażółć 🧪"},
		"items": []any{
			map[string]any{"id": "dev_seed", "type": "message", "role": "developer", "content": "Preserve exact state."},
			map[string]any{"id": "image_seed", "type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "turn zero"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,cWE=", "detail": "high", "x_vendor": map[string]any{"deep": []any{1, map[string]any{"two": 2}}}},
			}},
			map[string]any{"id": "reasoning_seed", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "kept summary"}}, "encrypted_content": "opaque-persisted"},
			map[string]any{"id": "future_seed", "type": "future_tool_v9", "payload": map[string]any{"nil": nil, "float": 1.25, "lambda": "λ"}, "x_extension": []any{"a", map[string]any{"b": true}}},
		},
	}, http.StatusOK)
	conversationID := created["id"].(string)

	// Mix metadata patches and item appends at the same instant. Both operations
	// touch the same logical conversation but different persisted fields.
	const metadataWriters = 8
	const itemWriters = 16
	start := make(chan struct{})
	results := make(chan conversationHTTPResult, metadataWriters+itemWriters)
	var wg sync.WaitGroup
	for index := range metadataWriters {
		wg.Go(func() {
			<-start
			results <- rawConversationRequest(fixture, http.MethodPost, "/v1/conversations/"+conversationID,
				map[string]any{"metadata": map[string]string{fmt.Sprintf("parallel_%d", index): fmt.Sprintf("value-%d", index)}})
		})
	}
	for index := range itemWriters {
		wg.Go(func() {
			<-start
			results <- rawConversationRequest(fixture, http.MethodPost, "/v1/conversations/"+conversationID+"/items", map[string]any{
				"items": []any{map[string]any{"id": fmt.Sprintf("parallel_item_%02d", index), "type": "message", "role": "user", "content": fmt.Sprintf("parallel %d", index), "sequence": index}},
			})
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status, string(result.body))
	}

	updated := conversationJSONRequest(t, fixture, http.MethodGet, "/v1/conversations/"+conversationID, nil, http.StatusOK)
	metadata := updated["metadata"].(map[string]any)
	require.Len(t, metadata, metadataWriters+2)
	for index := range metadataWriters {
		require.Equal(t, fmt.Sprintf("value-%d", index), metadata[fmt.Sprintf("parallel_%d", index)])
	}

	// Race identical explicit IDs. The store-level compare-and-append must leave
	// exactly one addressable item even when every handler read the same snapshot.
	const collisionWriters = 12
	start = make(chan struct{})
	collisions := make(chan conversationHTTPResult, collisionWriters)
	for range collisionWriters {
		wg.Go(func() {
			<-start
			collisions <- rawConversationRequest(fixture, http.MethodPost, "/v1/conversations/"+conversationID+"/items", map[string]any{
				"items": []any{map[string]any{"id": "shared_collision", "type": "message", "role": "user", "content": "same id"}},
			})
		})
	}
	close(start)
	wg.Wait()
	close(collisions)
	statusCounts := map[int]int{}
	for result := range collisions {
		require.NoError(t, result.err)
		statusCounts[result.status]++
	}
	require.Equal(t, 1, statusCounts[http.StatusOK], statusCounts)
	require.Equal(t, collisionWriters-1, statusCounts[http.StatusBadRequest], statusCounts)

	ascending := collectConversationItemIDs(t, fixture, conversationID, "asc", 7)
	require.Len(t, ascending, 4+itemWriters+1)
	require.Len(t, uniqueStrings(ascending), len(ascending))
	descending := collectConversationItemIDs(t, fixture, conversationID, "desc", 100)
	reversed := slices.Clone(ascending)
	slices.Reverse(reversed)
	require.Equal(t, reversed, descending)

	// Optional-field redaction must be a view, not a destructive rewrite.
	withoutInclude := conversationJSONRequest(t, fixture, http.MethodGet,
		"/v1/conversations/"+conversationID+"/items/reasoning_seed", nil, http.StatusOK)
	require.NotContains(t, withoutInclude, "encrypted_content")
	withInclude := conversationJSONRequest(t, fixture, http.MethodGet,
		"/v1/conversations/"+conversationID+"/items/reasoning_seed?include=reasoning.encrypted_content", nil, http.StatusOK)
	require.Equal(t, "opaque-persisted", withInclude["encrypted_content"])

	// Delete one old item while another writer appends. MongoDB uses a CAS and
	// PostgreSQL uses JSONB row updates; neither operation may undo the other.
	start = make(chan struct{})
	deleteAppend := make(chan conversationHTTPResult, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		deleteAppend <- rawConversationRequest(fixture, http.MethodDelete,
			"/v1/conversations/"+conversationID+"/items/future_seed", nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		deleteAppend <- rawConversationRequest(fixture, http.MethodPost,
			"/v1/conversations/"+conversationID+"/items", map[string]any{
				"items": []any{map[string]any{"id": "post_delete_append", "type": "future_after_delete", "payload": map[string]any{"kept": true}}},
			})
	}()
	close(start)
	wg.Wait()
	close(deleteAppend)
	for result := range deleteAppend {
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status, string(result.body))
	}
	conversationJSONRequest(t, fixture, http.MethodGet,
		"/v1/conversations/"+conversationID+"/items/future_seed", nil, http.StatusNotFound)
	conversationJSONRequest(t, fixture, http.MethodGet,
		"/v1/conversations/"+conversationID+"/items/post_delete_append", nil, http.StatusOK)

	// Three Responses turns exercise replay through a real provider adapter. The
	// integration mock intentionally reuses response and output IDs each turn;
	// persisted conversation IDs must still be unique and all turns retained.
	for turn := range 3 {
		conversationJSONRequest(t, fixture, http.MethodPost, "/v1/responses", map[string]any{
			"model":        "gpt-4",
			"conversation": map[string]any{"id": conversationID},
			"input": []any{map[string]any{
				"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": fmt.Sprintf("complex turn %d", turn)}},
				"phase":   "commentary",
			}},
			"reasoning":           map[string]any{"effort": "medium", "summary": "detailed"},
			"text":                map[string]any{"verbosity": "low"},
			"parallel_tool_calls": false,
			"store":               false,
		}, http.StatusOK)
	}
	afterTurns := collectConversationItemIDs(t, fixture, conversationID, "asc", 100)
	require.Len(t, afterTurns, len(ascending)+6)
	require.Len(t, uniqueStrings(afterTurns), len(afterTurns))

	assertConversationDatabaseState(t, fixture, conversationID, metadataWriters+2, len(afterTurns))
}

func rawConversationRequest(fixture *TestServerFixture, method, path string, payload any) conversationHTTPResult {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return conversationHTTPResult{err: err}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, fixture.ServerURL+path, body)
	if err != nil {
		return conversationHTTPResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+conversationIntegrationKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return conversationHTTPResult{err: err}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	return conversationHTTPResult{status: resp.StatusCode, body: responseBody, err: err}
}

func conversationJSONRequest(t *testing.T, fixture *TestServerFixture, method, path string, payload any, wantStatus int) map[string]any {
	t.Helper()
	result := rawConversationRequest(fixture, method, path, payload)
	require.NoError(t, result.err)
	require.Equal(t, wantStatus, result.status, string(result.body))
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(result.body, &decoded), string(result.body))
	return decoded
}

func collectConversationItemIDs(t *testing.T, fixture *TestServerFixture, conversationID, order string, limit int) []string {
	t.Helper()
	var result []string
	after := ""
	for {
		path := fmt.Sprintf("/v1/conversations/%s/items?order=%s&limit=%d", conversationID, order, limit)
		if after != "" {
			path += "&after=" + after
		}
		page := conversationJSONRequest(t, fixture, http.MethodGet, path, nil, http.StatusOK)
		data := page["data"].([]any)
		for _, raw := range data {
			result = append(result, raw.(map[string]any)["id"].(string))
		}
		if !page["has_more"].(bool) {
			return result
		}
		after = page["last_id"].(string)
	}
}

func uniqueStrings(values []string) map[string]struct{} {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	return unique
}

func assertConversationDatabaseState(t *testing.T, fixture *TestServerFixture, conversationID string, metadataCount, itemCount int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch fixture.DBType {
	case "postgresql":
		var data string
		var items []byte
		err := fixture.PgPool.QueryRow(ctx,
			"SELECT data, items::text FROM conversation_snapshots WHERE id = $1", conversationID,
		).Scan(&data, &items)
		require.NoError(t, err)
		require.Contains(t, string(items), "opaque-persisted")
		require.NotContains(t, string(items), "future_seed")
		var decodedItems []any
		require.NoError(t, json.Unmarshal(items, &decodedItems))
		require.Len(t, decodedItems, itemCount)
		var decodedData map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &decodedData))
		conversation := decodedData["conversation"].(map[string]any)
		require.Len(t, conversation["metadata"].(map[string]any), metadataCount)
	case "mongodb":
		var doc struct {
			Data  string   `bson:"data"`
			Items []string `bson:"items"`
		}
		err := fixture.MongoDb.Collection("conversation_snapshots").FindOne(ctx, bson.M{"_id": conversationID}).Decode(&doc)
		require.NoError(t, err)
		require.Len(t, doc.Items, itemCount)
		encodedItems, err := json.Marshal(doc.Items)
		require.NoError(t, err)
		require.Contains(t, string(encodedItems), "opaque-persisted")
		require.NotContains(t, string(encodedItems), "future_seed")
		var decodedData map[string]any
		require.NoError(t, json.Unmarshal([]byte(doc.Data), &decodedData))
		conversation := decodedData["conversation"].(map[string]any)
		require.Len(t, conversation["metadata"].(map[string]any), metadataCount)
	default:
		t.Fatalf("unsupported database type %q", fixture.DBType)
	}
}
