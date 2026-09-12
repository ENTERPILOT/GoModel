package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/require"
)

func TestFetchBatchResultsFromOutputFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/batches/batch_1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"batch_1","status":"completed","output_file_id":"file_1","endpoint":"/v1/chat/completions"}`))
		case "/files/file_1/content":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(
				`{"custom_id":"ok-1","response":{"status_code":200,"url":"/v1/chat/completions","body":{"id":"resp-1","model":"gpt-4o-mini","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}}}` + "\n" +
					`{"custom_id":"err-1","error":{"type":"invalid_request_error","message":"bad request"}}`,
			))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := llmclient.NewWithHTTPClient(server.Client(), llmclient.DefaultConfig("openai", server.URL), nil)
	resp, err := FetchBatchResultsFromOutputFile(context.Background(), client, "openai", "batch_1")
	require.NoError(t, err)
	require.Equal(t, "batch_1", resp.BatchID)
	require.Len(t, resp.Data, 2)
	require.Equal(t, 200, resp.Data[0].StatusCode)
	require.Equal(t, "gpt-4o-mini", resp.Data[0].Model, "unexpected first row: %+v", resp.Data[0])
	require.NotNil(t, resp.Data[1].Error)
	require.Equal(t, "invalid_request_error", resp.Data[1].Error.Type, "unexpected error row: %+v", resp.Data[1])
}

func TestFetchBatchResultsFromOutputFilePending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/batches/batch_2" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"batch_2","status":"in_progress"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := llmclient.NewWithHTTPClient(server.Client(), llmclient.DefaultConfig("openai", server.URL), nil)
	_, err := FetchBatchResultsFromOutputFile(context.Background(), client, "openai", "batch_2")
	require.Error(t, err)

	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	require.Equal(t, http.StatusConflict, gwErr.HTTPStatusCode())
}

func TestFetchBatchResultsFromOpenAICompatibleEndpoints_ReturnsProviderErrorOnNilBatchResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  *llmclient.Response
		wantError string
	}{
		{
			name:      "nil response",
			response:  nil,
			wantError: "provider returned empty batch response",
		},
		{
			name:      "nil body",
			response:  &llmclient.Response{StatusCode: http.StatusOK, Body: nil},
			wantError: "provider returned empty batch response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetchBatchResultsFromOpenAICompatibleEndpoints(
				context.Background(),
				"openai",
				"batch_1",
				"",
				func(context.Context, llmclient.Request) (*llmclient.Response, error) {
					return tt.response, nil
				},
				func(context.Context, llmclient.Request) (*http.Response, error) {
					t.Fatal("unexpected passthrough call")
					return nil, nil
				},
			)
			require.Error(t, err)

			var gwErr *core.GatewayError
			require.ErrorAs(t, err, &gwErr)
			require.Equal(t, core.ErrorTypeProvider, gwErr.Type)
			require.Contains(t, gwErr.Message, tt.wantError)
		})
	}
}
