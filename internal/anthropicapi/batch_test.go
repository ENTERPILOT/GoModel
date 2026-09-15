package anthropicapi

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestDecodeBatchCreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		items   int
	}{
		{
			name:  "valid",
			body:  `{"requests":[{"custom_id":"a","params":{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}}]}`,
			items: 1,
		},
		{name: "empty body", body: "", wantErr: true},
		{name: "trailing garbage", body: `{"requests":[]}{"x":1}`, wantErr: true},
		{name: "not an object", body: `[1,2]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := DecodeBatchCreateRequest([]byte(tc.body))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.items, len(req.Requests))
		})
	}
}

func TestToBatchRequest(t *testing.T) {
	params := `{"model":"claude-haiku","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	tests := []struct {
		name    string
		req     *BatchCreateRequest
		wantErr string
	}{
		{
			name: "valid",
			req: &BatchCreateRequest{Requests: []BatchCreateItem{
				{CustomID: "a", Params: json.RawMessage(params)},
				{CustomID: "b", Params: json.RawMessage(params)},
			}},
		},
		{name: "nil request", req: nil, wantErr: "requests must not be empty"},
		{
			name:    "empty requests",
			req:     &BatchCreateRequest{},
			wantErr: "requests must not be empty",
		},
		{
			name: "missing custom_id",
			req: &BatchCreateRequest{Requests: []BatchCreateItem{
				{Params: json.RawMessage(params)},
			}},
			wantErr: "requests[0].custom_id is required",
		},
		{
			name: "duplicate custom_id",
			req: &BatchCreateRequest{Requests: []BatchCreateItem{
				{CustomID: "a", Params: json.RawMessage(params)},
				{CustomID: "a", Params: json.RawMessage(params)},
			}},
			wantErr: `requests[1].custom_id "a" is not unique`,
		},
		{
			name: "invalid params carry the item index",
			req: &BatchCreateRequest{Requests: []BatchCreateItem{
				{CustomID: "a", Params: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)},
			}},
			wantErr: "requests[0].params: max_tokens must be a positive integer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ToBatchRequest(tc.req)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "/v1/chat/completions", out.Endpoint)
			require.Equal(t, "24h", out.CompletionWindow)
			require.Len(t, out.Requests, 2)

			item := out.Requests[0]
			require.Equal(t, "a", item.CustomID)
			require.Equal(t, "POST", item.Method)
			require.Equal(t, "/v1/chat/completions", item.URL, "item = %+v", item)

			var body map[string]any
			err = json.Unmarshal(item.Body, &body)
			require.NoError(t, err)
			require.Equal(t, "claude-haiku", body["model"])
			require.Equal(t, float64(32), body["max_tokens"], "translated body = %v", body)
		})
	}
}

func TestMessageBatchIDMapping(t *testing.T) {
	got := MessageBatchID("batch_123")
	require.Equal(t, "msgbatch_123", got)
	got = GatewayBatchID("msgbatch_123")
	require.Equal(t, "batch_123", got)
	// Unprefixed IDs pass through unchanged in both directions.
	got = MessageBatchID("other_1")
	require.Equal(t, "other_1", got)
	got = GatewayBatchID("other_1")
	require.Equal(t, "other_1", got)
}

func TestFromBatchResponse(t *testing.T) {
	completedAt := int64(2000)
	cancellingAt := int64(1500)
	tests := []struct {
		name           string
		batch          *core.BatchResponse
		wantStatus     string
		wantCounts     MessageBatchRequestCounts
		wantResultsURL bool
	}{
		{
			name: "in progress",
			batch: &core.BatchResponse{
				ID: "batch_1", Status: "in_progress", CreatedAt: 1000,
				RequestCounts: core.BatchRequestCounts{Total: 3, Completed: 1},
			},
			wantStatus: "in_progress",
			wantCounts: MessageBatchRequestCounts{Processing: 2, Succeeded: 1},
		},
		{
			name: "completed",
			batch: &core.BatchResponse{
				ID: "batch_1", Status: "completed", CreatedAt: 1000, CompletedAt: &completedAt,
				RequestCounts: core.BatchRequestCounts{Total: 3, Completed: 2, Failed: 1},
			},
			wantStatus:     "ended",
			wantCounts:     MessageBatchRequestCounts{Succeeded: 2, Errored: 1},
			wantResultsURL: true,
		},
		{
			name: "cancelling",
			batch: &core.BatchResponse{
				ID: "batch_1", Status: "cancelling", CreatedAt: 1000, CancellingAt: &cancellingAt,
				RequestCounts: core.BatchRequestCounts{Total: 2},
			},
			wantStatus: "canceling",
			wantCounts: MessageBatchRequestCounts{Processing: 2},
		},
		{
			name: "cancelled remainder counts as canceled",
			batch: &core.BatchResponse{
				ID: "batch_1", Status: "cancelled", CreatedAt: 1000,
				RequestCounts: core.BatchRequestCounts{Total: 3, Completed: 1},
			},
			wantStatus:     "ended",
			wantCounts:     MessageBatchRequestCounts{Succeeded: 1, Canceled: 2},
			wantResultsURL: true,
		},
		{
			name: "expired remainder counts as expired",
			batch: &core.BatchResponse{
				ID: "batch_1", Status: "expired", CreatedAt: 1000,
				RequestCounts: core.BatchRequestCounts{Total: 2, Completed: 1},
			},
			wantStatus:     "ended",
			wantCounts:     MessageBatchRequestCounts{Succeeded: 1, Expired: 1},
			wantResultsURL: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := FromBatchResponse(tc.batch)
			require.Equal(t, "msgbatch_1", out.ID)
			require.Equal(t, "message_batch", out.Type)
			require.Equal(t, tc.wantStatus, out.ProcessingStatus)
			require.Equal(t, tc.wantCounts, out.RequestCounts)
			require.Equal(t, "1970-01-01T00:16:40Z", out.CreatedAt)

			// expires_at is created_at + the 24h completion window.
			require.Equal(t, "1970-01-02T00:16:40Z", out.ExpiresAt)

			if tc.wantResultsURL {
				require.NotNil(t, out.ResultsURL)
				require.Equal(t, "/v1/messages/batches/msgbatch_1/results", *out.ResultsURL)
			} else {
				require.Nil(t, out.ResultsURL, "results_url should be null while %s", tc.wantStatus)
			}
			if tc.batch.CancellingAt != nil {
				require.NotNil(t, out.CancelInitiatedAt, "cancel_initiated_at missing")
			}
			// ended_at reflects a provider-reported timestamp and is never
			// fabricated at render time.
			if tc.batch.CompletedAt != nil {
				require.NotNil(t, out.EndedAt)
				require.Equal(t, "1970-01-01T00:33:20Z", *out.EndedAt)
			} else {
				require.Nil(t, out.EndedAt, "ended_at fabricated without a provider timestamp")
			}
		})
	}
}

func TestFromBatchList(t *testing.T) {
	list := &core.BatchListResponse{
		HasMore: true,
		Data: []core.BatchResponse{
			{ID: "batch_a", Status: "in_progress", CreatedAt: 1},
			{ID: "batch_b", Status: "completed", CreatedAt: 2},
		},
	}
	out := FromBatchList(list)
	require.Len(t, out.Data, 2)
	require.True(t, out.HasMore, "list = %+v", out)
	require.Equal(t, "msgbatch_a", *out.FirstID)
	require.Equal(t, "msgbatch_b", *out.LastID)
}

func TestEncodeBatchResults(t *testing.T) {
	chat := &core.ChatResponse{
		ID:    "resp-1",
		Model: "claude-haiku",
		Choices: []core.Choice{{
			Message:      core.ResponseMessage{Role: "assistant", Content: "hello"},
			FinishReason: "stop",
		}},
		Usage: core.Usage{PromptTokens: 3, CompletionTokens: 2},
	}
	// OpenAI-compatible providers store the decoded chat body as a map.
	var chatAsMap map[string]any
	raw, _ := json.Marshal(chat)
	_ = json.Unmarshal(raw, &chatAsMap)

	results := &core.BatchResultsResponse{
		BatchID: "batch_1",
		Data: []core.BatchResultItem{
			{CustomID: "typed", StatusCode: 200, Response: chat},
			{CustomID: "mapped", StatusCode: 200, Response: chatAsMap},
			{CustomID: "failed", StatusCode: 400, Error: &core.BatchError{Type: "invalid_request_error", Message: "bad item"}},
			{CustomID: "gone", StatusCode: 400, Error: &core.BatchError{Type: "canceled", Message: "batch item failed"}},
			{CustomID: "late", StatusCode: 400, Error: &core.BatchError{Type: "expired", Message: "batch item failed"}},
		},
	}
	payload, err := EncodeBatchResults(results)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	require.Len(t, lines, 5)

	decoded := make(map[string]map[string]any, len(lines))
	for _, line := range lines {
		var row struct {
			CustomID string         `json:"custom_id"`
			Result   map[string]any `json:"result"`
		}
		err := json.Unmarshal([]byte(line), &row)
		require.NoError(t, err, "line %q: %v", line, err)

		decoded[row.CustomID] = row.Result
	}

	for _, id := range []string{"typed", "mapped"} {
		result := decoded[id]
		require.Equal(t, "succeeded", result["type"], "%s type = %v", id, result["type"])

		message := result["message"].(map[string]any)
		require.Equal(t, "message", message["type"])
		require.Equal(t, "msg_resp-1", message["id"], "%s message = %v", id, message)

		content := message["content"].([]any)
		require.Equal(t, "hello", content[0].(map[string]any)["text"], "%s content = %v", id, content)
	}

	failed := decoded["failed"]
	require.Equal(t, "errored", failed["type"])

	envelope := failed["error"].(map[string]any)
	inner := envelope["error"].(map[string]any)
	require.Equal(t, "error", envelope["type"])
	require.Equal(t, "invalid_request_error", inner["type"])
	require.Equal(t, "bad item", inner["message"], "failed envelope = %v", envelope)
	require.Equal(t, "canceled", decoded["gone"]["type"])
	require.Equal(t, "expired", decoded["late"]["type"], "canceled/expired mapping = %v / %v", decoded["gone"], decoded["late"])
}
