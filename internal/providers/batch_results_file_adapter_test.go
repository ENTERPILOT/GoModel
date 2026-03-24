package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.BatchID != "batch_1" {
		t.Fatalf("BatchID = %q, want %q", resp.BatchID, "batch_1")
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].StatusCode != 200 || resp.Data[0].Model != "gpt-4o-mini" {
		t.Fatalf("unexpected first row: %+v", resp.Data[0])
	}
	if resp.Data[1].Error == nil || resp.Data[1].Error.Type != "invalid_request_error" {
		t.Fatalf("unexpected error row: %+v", resp.Data[1])
	}
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
	if err == nil {
		t.Fatal("expected error")
	}
	var gwErr *core.GatewayError
	if !errors.As(err, &gwErr) {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gwErr.HTTPStatusCode() != http.StatusConflict {
		t.Fatalf("status = %d, want %d", gwErr.HTTPStatusCode(), http.StatusConflict)
	}
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
			if err == nil {
				t.Fatal("expected error")
			}
			var gwErr *core.GatewayError
			if !errors.As(err, &gwErr) {
				t.Fatalf("expected GatewayError, got %T: %v", err, err)
			}
			if gwErr.Type != core.ErrorTypeProvider {
				t.Fatalf("error type = %q, want %q", gwErr.Type, core.ErrorTypeProvider)
			}
			if !strings.Contains(gwErr.Message, tt.wantError) {
				t.Fatalf("message = %q, want substring %q", gwErr.Message, tt.wantError)
			}
		})
	}
}
