package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

func messagesBatchMock() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"claude-3-haiku-20240307"},
		providerTypes: map[string]string{
			"claude-3-haiku-20240307": "anthropic",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "provider-batch-1",
			Object:        "batch",
			Status:        "in_progress",
			CreatedAt:     1000,
			RequestCounts: core.BatchRequestCounts{Total: 2},
		},
	}
}

const messagesBatchCreateBody = `{
  "requests": [
    {"custom_id": "first", "params": {"model": "claude-3-haiku-20240307", "max_tokens": 32, "messages": [{"role": "user", "content": "hi"}]}},
    {"custom_id": "second", "params": {"model": "claude-3-haiku-20240307", "max_tokens": 32, "messages": [{"role": "user", "content": "hello"}]}}
  ]
}`

func createMessagesBatch(t *testing.T, e *echo.Echo, handler *Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/batches", strings.NewReader(messagesBatchCreateBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := handler.MessagesBatches(c); err != nil {
		t.Fatalf("create handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created
}

func TestMessagesBatches_CreateGetList(t *testing.T) {
	mock := messagesBatchMock()
	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	created := createMessagesBatch(t, e, handler)
	id, _ := created["id"].(string)
	if !strings.HasPrefix(id, "msgbatch_") {
		t.Fatalf("id = %q, want msgbatch_ prefix", id)
	}
	if created["type"] != "message_batch" || created["processing_status"] != "in_progress" {
		t.Fatalf("created = %v", created)
	}
	counts := created["request_counts"].(map[string]any)
	if counts["processing"] != float64(2) || counts["succeeded"] != float64(0) {
		t.Fatalf("request_counts = %v", counts)
	}
	if created["results_url"] != nil {
		t.Fatalf("results_url should be null while in progress, got %v", created["results_url"])
	}

	// The provider received translated canonical chat items.
	if mock.capturedBatchReq == nil || len(mock.capturedBatchReq.Requests) != 2 {
		t.Fatalf("capturedBatchReq = %+v", mock.capturedBatchReq)
	}
	item := mock.capturedBatchReq.Requests[0]
	if item.CustomID != "first" || item.URL != "/v1/chat/completions" {
		t.Fatalf("item = %+v", item)
	}

	// Retrieve through the msgbatch_ alias.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/messages/batches/"+id, nil)
	getRec := httptest.NewRecorder()
	getCtx := e.NewContext(getReq, getRec)
	getCtx.SetPath("/v1/messages/batches/:id")
	setPathParam(getCtx, "id", id)
	if err := handler.GetMessagesBatch(getCtx); err != nil {
		t.Fatalf("get handler returned error: %v", err)
	}
	var fetched map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched["id"] != id {
		t.Fatalf("get id = %v, want %v", fetched["id"], id)
	}

	// List renders msgbatch_ IDs.
	listReq := httptest.NewRequest(http.MethodGet, "/v1/messages/batches", nil)
	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)
	if err := handler.ListMessagesBatches(listCtx); err != nil {
		t.Fatalf("list handler returned error: %v", err)
	}
	var list struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0]["id"] != id {
		t.Fatalf("list = %+v", list)
	}
}

func TestMessagesBatches_Results(t *testing.T) {
	mock := messagesBatchMock()
	mock.batchResults = &core.BatchResultsResponse{
		Object:  "list",
		BatchID: "provider-batch-1",
		Data: []core.BatchResultItem{
			{
				CustomID:   "first",
				StatusCode: 200,
				Response: &core.ChatResponse{
					ID:    "resp-1",
					Model: "claude-3-haiku-20240307",
					Choices: []core.Choice{{
						Message:      core.ResponseMessage{Role: "assistant", Content: "hello"},
						FinishReason: "stop",
					}},
				},
			},
			{
				CustomID:   "second",
				StatusCode: 400,
				Error:      &core.BatchError{Type: "invalid_request_error", Message: "boom"},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	created := createMessagesBatch(t, e, handler)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/v1/messages/batches/"+id+"/results", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/messages/batches/:id/results")
	setPathParam(c, "id", id)
	if err := handler.MessagesBatchResults(c); err != nil {
		t.Fatalf("results handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("results status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/x-jsonl") {
		t.Fatalf("content-type = %q", got)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d body=%s", len(lines), rec.Body.String())
	}
	var succeeded struct {
		CustomID string `json:"custom_id"`
		Result   struct {
			Type    string `json:"type"`
			Message struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &succeeded); err != nil {
		t.Fatalf("decode succeeded line: %v", err)
	}
	if succeeded.CustomID != "first" || succeeded.Result.Type != "succeeded" ||
		succeeded.Result.Message.Type != "message" || succeeded.Result.Message.Content[0].Text != "hello" {
		t.Fatalf("succeeded line = %+v", succeeded)
	}
}

func TestMessagesBatches_Cancel(t *testing.T) {
	mock := messagesBatchMock()
	mock.batchCancelResponse = &core.BatchResponse{
		ID:        "provider-batch-1",
		Object:    "batch",
		Status:    "cancelling",
		CreatedAt: 1000,
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	created := createMessagesBatch(t, e, handler)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/batches/"+id+"/cancel", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/messages/batches/:id/cancel")
	setPathParam(c, "id", id)
	if err := handler.CancelMessagesBatch(c); err != nil {
		t.Fatalf("cancel handler returned error: %v", err)
	}
	var canceled map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &canceled); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if canceled["processing_status"] != "canceling" {
		t.Fatalf("processing_status = %v", canceled["processing_status"])
	}
}

func TestMessagesBatches_Delete(t *testing.T) {
	tests := []struct {
		name       string
		getStatus  string
		wantStatus int
	}{
		{name: "ended batch deletes", getStatus: "completed", wantStatus: http.StatusOK},
		{name: "in-progress batch is rejected", getStatus: "in_progress", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := messagesBatchMock()
			mock.batchGetResponse = &core.BatchResponse{
				ID:            "provider-batch-1",
				Object:        "batch",
				Status:        tc.getStatus,
				CreatedAt:     1000,
				RequestCounts: core.BatchRequestCounts{Total: 2, Completed: 2},
			}

			e := echo.New()
			handler := NewHandler(mock, nil, nil, nil)
			created := createMessagesBatch(t, e, handler)
			id := created["id"].(string)

			req := httptest.NewRequest(http.MethodDelete, "/v1/messages/batches/"+id, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/v1/messages/batches/:id")
			setPathParam(c, "id", id)
			if err := handler.DeleteMessagesBatch(c); err != nil {
				t.Fatalf("delete handler returned error: %v", err)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				// The Anthropic dialect renders the canonical error envelope.
				if !strings.Contains(rec.Body.String(), `"type":"error"`) {
					t.Fatalf("error body = %s", rec.Body.String())
				}
				return
			}
			var deleted map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
				t.Fatalf("decode delete response: %v", err)
			}
			if deleted["id"] != id || deleted["type"] != "message_batch_deleted" {
				t.Fatalf("deleted = %v", deleted)
			}

			// The batch is gone afterwards.
			getReq := httptest.NewRequest(http.MethodGet, "/v1/messages/batches/"+id, nil)
			getRec := httptest.NewRecorder()
			getCtx := e.NewContext(getReq, getRec)
			getCtx.SetPath("/v1/messages/batches/:id")
			setPathParam(getCtx, "id", id)
			if err := handler.GetMessagesBatch(getCtx); err != nil {
				t.Fatalf("get handler returned error: %v", err)
			}
			if getRec.Code != http.StatusNotFound {
				t.Fatalf("get after delete = %d", getRec.Code)
			}
		})
	}
}

func TestMessagesBatches_InvalidCreateReturnsAnthropicError(t *testing.T) {
	e := echo.New()
	handler := NewHandler(messagesBatchMock(), nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/batches",
		strings.NewReader(`{"requests":[{"custom_id":"a","params":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := handler.MessagesBatches(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Type != "error" || envelope.Error.Type != "invalid_request_error" ||
		!strings.Contains(envelope.Error.Message, "requests[0].params") {
		t.Fatalf("envelope = %+v", envelope)
	}
}
