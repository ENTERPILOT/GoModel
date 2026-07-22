//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type conversationAPIObject struct {
	ID       string            `json:"id"`
	Object   string            `json:"object"`
	Metadata map[string]string `json:"metadata"`
	Deleted  bool              `json:"deleted"`
	Data     []json.RawMessage `json:"data"`
	FirstID  string            `json:"first_id"`
	LastID   string            `json:"last_id"`
	HasMore  bool              `json:"has_more"`
}

// TestConversationsOfficialRoutes_E2E exercises every Conversations operation
// exposed by the official OpenAI SDK through the real router and auth stack.
func TestConversationsOfficialRoutes_E2E(t *testing.T) {
	const key = "sk-e2e-conversations"
	httpServer := httptest.NewServer(setupAuthServer(t, key))
	t.Cleanup(httpServer.Close)

	call := func(method, path, body string, target any) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		req, err := http.NewRequest(method, httpServer.URL+path, reader)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equalf(t, http.StatusOK, resp.StatusCode, "%s %s: %s", method, path, payload)
		if target != nil {
			require.NoError(t, json.Unmarshal(payload, target))
		}
	}

	var created conversationAPIObject
	call(http.MethodPost, "/v1/conversations", `{
		"metadata":{"suite":"sdk","retained":"yes"},
		"items":[
			{"role":"developer","content":"Preserve exact JSON fields."},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}],"phase":"commentary"}
		]
	}`, &created)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "conversation", created.Object)

	var retrieved conversationAPIObject
	call(http.MethodGet, "/v1/conversations/"+created.ID, "", &retrieved)
	require.Equal(t, created.ID, retrieved.ID)

	var updated conversationAPIObject
	call(http.MethodPost, "/v1/conversations/"+created.ID, `{"metadata":{"suite":"updated"}}`, &updated)
	require.Equal(t, "updated", updated.Metadata["suite"])
	require.Equal(t, "yes", updated.Metadata["retained"])

	var firstPage conversationAPIObject
	call(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc&limit=1&include=reasoning.encrypted_content", "", &firstPage)
	require.Len(t, firstPage.Data, 1)
	require.True(t, firstPage.HasMore)
	require.NotEmpty(t, firstPage.LastID)

	var secondPage conversationAPIObject
	call(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc&limit=10&after="+firstPage.LastID, "", &secondPage)
	require.Len(t, secondPage.Data, 1)
	require.False(t, secondPage.HasMore)

	var added conversationAPIObject
	call(http.MethodPost, "/v1/conversations/"+created.ID+"/items?include=reasoning.encrypted_content", `{
		"items":[
			{"type":"function_call","call_id":"call_e2e","name":"lookup","arguments":{"nested":true}},
			{"type":"function_call_output","call_id":"call_e2e","output":[{"type":"input_text","text":"done"}]},
			{"type":"reasoning","summary":[],"encrypted_content":"opaque-e2e"}
		]
	}`, &added)
	require.Len(t, added.Data, 3)
	require.NotEmpty(t, added.LastID)

	var item map[string]any
	call(http.MethodGet, "/v1/conversations/"+created.ID+"/items/"+added.LastID+"?include=reasoning.encrypted_content", "", &item)
	require.Equal(t, "reasoning", item["type"])
	require.Equal(t, "opaque-e2e", item["encrypted_content"])

	var afterItemDelete conversationAPIObject
	call(http.MethodDelete, "/v1/conversations/"+created.ID+"/items/"+added.LastID, "", &afterItemDelete)
	require.Equal(t, created.ID, afterItemDelete.ID)

	var deleted conversationAPIObject
	call(http.MethodDelete, "/v1/conversations/"+created.ID, "", &deleted)
	require.Equal(t, "conversation.deleted", deleted.Object)
	require.True(t, deleted.Deleted)
}
