package anthropicapi

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

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
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(req.Requests) != tc.items {
				t.Fatalf("len(requests) = %d, want %d", len(req.Requests), tc.items)
			}
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
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Endpoint != "/v1/chat/completions" || out.CompletionWindow != "24h" {
				t.Fatalf("endpoint/window = %q/%q", out.Endpoint, out.CompletionWindow)
			}
			if len(out.Requests) != 2 {
				t.Fatalf("len(items) = %d, want 2", len(out.Requests))
			}
			item := out.Requests[0]
			if item.CustomID != "a" || item.Method != "POST" || item.URL != "/v1/chat/completions" {
				t.Fatalf("item = %+v", item)
			}
			var body map[string]any
			if err := json.Unmarshal(item.Body, &body); err != nil {
				t.Fatalf("item body: %v", err)
			}
			if body["model"] != "claude-haiku" || body["max_tokens"] != float64(32) {
				t.Fatalf("translated body = %v", body)
			}
		})
	}
}

func TestMessageBatchIDMapping(t *testing.T) {
	if got := MessageBatchID("batch_123"); got != "msgbatch_123" {
		t.Fatalf("MessageBatchID = %q", got)
	}
	if got := GatewayBatchID("msgbatch_123"); got != "batch_123" {
		t.Fatalf("GatewayBatchID = %q", got)
	}
	// Unprefixed IDs pass through unchanged in both directions.
	if got := MessageBatchID("other_1"); got != "other_1" {
		t.Fatalf("MessageBatchID passthrough = %q", got)
	}
	if got := GatewayBatchID("other_1"); got != "other_1" {
		t.Fatalf("GatewayBatchID passthrough = %q", got)
	}
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
			if out.ID != "msgbatch_1" || out.Type != "message_batch" {
				t.Fatalf("id/type = %q/%q", out.ID, out.Type)
			}
			if out.ProcessingStatus != tc.wantStatus {
				t.Fatalf("processing_status = %q, want %q", out.ProcessingStatus, tc.wantStatus)
			}
			if out.RequestCounts != tc.wantCounts {
				t.Fatalf("request_counts = %+v, want %+v", out.RequestCounts, tc.wantCounts)
			}
			if out.CreatedAt != "1970-01-01T00:16:40Z" {
				t.Fatalf("created_at = %q", out.CreatedAt)
			}
			// expires_at is created_at + the 24h completion window.
			if out.ExpiresAt != "1970-01-02T00:16:40Z" {
				t.Fatalf("expires_at = %q", out.ExpiresAt)
			}
			if tc.wantResultsURL {
				if out.ResultsURL == nil || *out.ResultsURL != "/v1/messages/batches/msgbatch_1/results" {
					t.Fatalf("results_url = %v", out.ResultsURL)
				}
			} else if out.ResultsURL != nil {
				t.Fatalf("results_url should be null while %s", tc.wantStatus)
			}
			if tc.batch.CancellingAt != nil && out.CancelInitiatedAt == nil {
				t.Fatal("cancel_initiated_at missing")
			}
			// ended_at reflects a provider-reported timestamp and is never
			// fabricated at render time.
			if tc.batch.CompletedAt != nil {
				if out.EndedAt == nil || *out.EndedAt != "1970-01-01T00:33:20Z" {
					t.Fatalf("ended_at = %v", out.EndedAt)
				}
			} else if out.EndedAt != nil {
				t.Fatalf("ended_at fabricated without a provider timestamp: %v", *out.EndedAt)
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
	if len(out.Data) != 2 || !out.HasMore {
		t.Fatalf("list = %+v", out)
	}
	if *out.FirstID != "msgbatch_a" || *out.LastID != "msgbatch_b" {
		t.Fatalf("first/last = %v/%v", *out.FirstID, *out.LastID)
	}
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
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) != 5 {
		t.Fatalf("len(lines) = %d, want 5", len(lines))
	}

	decoded := make(map[string]map[string]any, len(lines))
	for _, line := range lines {
		var row struct {
			CustomID string         `json:"custom_id"`
			Result   map[string]any `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		decoded[row.CustomID] = row.Result
	}

	for _, id := range []string{"typed", "mapped"} {
		result := decoded[id]
		if result["type"] != "succeeded" {
			t.Fatalf("%s type = %v", id, result["type"])
		}
		message := result["message"].(map[string]any)
		if message["type"] != "message" || message["id"] != "msg_resp-1" {
			t.Fatalf("%s message = %v", id, message)
		}
		content := message["content"].([]any)
		if content[0].(map[string]any)["text"] != "hello" {
			t.Fatalf("%s content = %v", id, content)
		}
	}

	failed := decoded["failed"]
	if failed["type"] != "errored" {
		t.Fatalf("failed type = %v", failed["type"])
	}
	envelope := failed["error"].(map[string]any)
	inner := envelope["error"].(map[string]any)
	if envelope["type"] != "error" || inner["type"] != "invalid_request_error" || inner["message"] != "bad item" {
		t.Fatalf("failed envelope = %v", envelope)
	}

	if decoded["gone"]["type"] != "canceled" || decoded["late"]["type"] != "expired" {
		t.Fatalf("canceled/expired mapping = %v / %v", decoded["gone"], decoded["late"])
	}
}
