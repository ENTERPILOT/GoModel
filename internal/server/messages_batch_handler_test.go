package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

func createMessagesBatch(t *testing.T, handler *Handler) map[string]any {
	t.Helper()
	c, rec := echotest.Post(t, "/v1/messages/batches", messagesBatchCreateBody)
	err := handler.MessagesBatches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	return echotest.Decode[map[string]any](t, rec)
}

func TestMessagesBatches_CreateGetList(t *testing.T) {
	mock := messagesBatchMock()
	handler := NewHandler(mock, nil, nil, nil)

	created := createMessagesBatch(t, handler)
	id, _ := created["id"].(string)
	require.True(t, strings.HasPrefix(id, "msgbatch_"), "id = %q, want msgbatch_ prefix", id)
	assert.Equal(t, "message_batch", created["type"])
	assert.Equal(t, "in_progress", created["processing_status"])

	counts := created["request_counts"].(map[string]any)
	assert.Equal(t, float64(2), counts["processing"])
	assert.Equal(t, float64(0), counts["succeeded"])
	assert.Nil(t, created["results_url"])

	// The provider received translated canonical chat items.
	require.NotNil(t, mock.capturedBatchReq)
	require.Len(t, mock.capturedBatchReq.Requests, 2)
	assert.Equal(t, core.RequestDialectAnthropicMessages, core.RequestDialectFromContext(mock.capturedBatchCtx))

	item := mock.capturedBatchReq.Requests[0]
	assert.Equal(t, "first", item.CustomID)
	assert.Equal(t, "/v1/chat/completions", item.URL)

	// Retrieve through the msgbatch_ alias.
	getCtx, getRec := echotest.Get(t, "/v1/messages/batches/"+id,
		echotest.WithPath("/v1/messages/batches/:id"), echotest.WithPathValue("id", id))
	err := handler.GetMessagesBatch(getCtx)
	require.NoError(t, err)
	fetched := echotest.Decode[map[string]any](t, getRec)
	assert.Equal(t, id, fetched["id"])

	// List renders msgbatch_ IDs.
	listCtx, listRec := echotest.Get(t, "/v1/messages/batches")
	err = handler.ListMessagesBatches(listCtx)
	require.NoError(t, err)

	list := echotest.Decode[struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
	}](t, listRec)
	require.Len(t, list.Data, 1)
	assert.Equal(t, id, list.Data[0]["id"])
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

	handler := NewHandler(mock, nil, nil, nil)
	created := createMessagesBatch(t, handler)
	id := created["id"].(string)

	c, rec := echotest.Get(t, "/v1/messages/batches/"+id+"/results",
		echotest.WithPath("/v1/messages/batches/:id/results"), echotest.WithPathValue("id", id))
	err := handler.MessagesBatchResults(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-jsonl"), "content-type = %q", rec.Header().Get("Content-Type"))

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	require.Len(t, lines, 2)

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
	err = json.Unmarshal([]byte(lines[0]), &succeeded)
	require.NoError(t, err)
	assert.Equal(t, "first", succeeded.CustomID)
	assert.Equal(t, "succeeded", succeeded.Result.Type)
	assert.Equal(t, "message", succeeded.Result.Message.Type)
	require.Len(t, succeeded.Result.Message.Content, 1)
	assert.Equal(t, "hello", succeeded.Result.Message.Content[0].Text)
}

func TestMessagesBatches_Cancel(t *testing.T) {
	mock := messagesBatchMock()
	mock.batchCancelResponse = &core.BatchResponse{
		ID:        "provider-batch-1",
		Object:    "batch",
		Status:    "cancelling",
		CreatedAt: 1000,
	}

	handler := NewHandler(mock, nil, nil, nil)
	created := createMessagesBatch(t, handler)
	id := created["id"].(string)

	c, rec := echotest.Post(t, "/v1/messages/batches/"+id+"/cancel", nil,
		echotest.WithPath("/v1/messages/batches/:id/cancel"), echotest.WithPathValue("id", id))
	err := handler.CancelMessagesBatch(c)
	require.NoError(t, err)

	canceled := echotest.Decode[map[string]any](t, rec)
	assert.Equal(t, "canceling", canceled["processing_status"])
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

			handler := NewHandler(mock, nil, nil, nil)
			created := createMessagesBatch(t, handler)
			id := created["id"].(string)

			c, rec := echotest.Request(t, http.MethodDelete, "/v1/messages/batches/"+id, nil,
				echotest.WithPath("/v1/messages/batches/:id"), echotest.WithPathValue("id", id))
			err := handler.DeleteMessagesBatch(c)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())

			if tc.wantStatus != http.StatusOK {
				// The Anthropic dialect renders the canonical error envelope.
				assert.Contains(t, rec.Body.String(), `"type":"error"`)
				return
			}
			deleted := echotest.Decode[map[string]any](t, rec)
			assert.Equal(t, id, deleted["id"])
			assert.Equal(t, "message_batch_deleted", deleted["type"])

			// The batch is gone afterwards.
			getCtx, getRec := echotest.Get(t, "/v1/messages/batches/"+id,
				echotest.WithPath("/v1/messages/batches/:id"), echotest.WithPathValue("id", id))
			err = handler.GetMessagesBatch(getCtx)
			require.NoError(t, err)
			assert.Equal(t, http.StatusNotFound, getRec.Code)
		})
	}
}

func TestMessagesBatches_InvalidCreateReturnsAnthropicError(t *testing.T) {
	handler := NewHandler(messagesBatchMock(), nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/messages/batches",
		`{"requests":[{"custom_id":"a","params":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]}`)
	err := handler.MessagesBatches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	envelope := echotest.Decode[struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, rec)
	assert.Equal(t, "error", envelope.Type)
	assert.Equal(t, "invalid_request_error", envelope.Error.Type)
	assert.Contains(t, envelope.Error.Message, "requests[0].params")
}
