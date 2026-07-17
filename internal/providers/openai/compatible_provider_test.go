package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestCompatibleProvider_ListModels_ReturnsUpstreamOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","owned_by":"openai"}]}`))
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "upstream-only",
			BaseURL:      server.URL,
		},
	)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "gpt-4o" {
		t.Fatalf("unexpected models: %+v", resp.Data)
	}
}

func TestCompatibleProvider_ListModels_DefaultsMissingObjectFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openrouter/model","object":"","owned_by":"openrouter"}]}`))
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "openrouter",
			BaseURL:      server.URL,
		},
	)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if resp.Object != "list" {
		t.Fatalf("response object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("model count = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "model" {
		t.Fatalf("model object = %q, want model", resp.Data[0].Object)
	}
}

func TestCompatibleProvider_ListModels_ReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "test-provider",
			BaseURL:      server.URL,
		},
	)

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error when upstream fails, got nil")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider && gatewayErr.Type != core.ErrorTypeNotFound {
		t.Errorf("gatewayErr.Type = %q, want provider_error or not_found_error", gatewayErr.Type)
	}
}

func TestCompatibleProvider_AdaptChatRequest_RewritesBodyOnChatAndStream(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","model":"quirk-1","choices":[]}`))
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "quirky",
			BaseURL:      server.URL,
			AdaptChatRequest: func(req *core.ChatRequest) (*core.ChatRequest, error) {
				adapted := *req
				adapted.User = "adapted-user"
				return &adapted, nil
			},
		},
	)

	original := &core.ChatRequest{Model: "quirk-1", User: "original-user"}
	if _, err := provider.ChatCompletion(context.Background(), original); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	stream, err := provider.StreamChatCompletion(context.Background(), original)
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	_, _ = io.ReadAll(stream)
	stream.Close()

	if len(bodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(bodies))
	}
	for i, body := range bodies {
		if !strings.Contains(body, `"adapted-user"`) {
			t.Fatalf("request %d body = %s, want adapted user", i, body)
		}
	}
	if original.User != "original-user" {
		t.Fatalf("original request mutated: User = %q", original.User)
	}
}

func TestCompatibleProvider_AdaptChatRequest_ErrorAborts(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "quirky",
			BaseURL:      server.URL,
			AdaptChatRequest: func(*core.ChatRequest) (*core.ChatRequest, error) {
				return nil, core.NewInvalidRequestError("cannot adapt", nil)
			},
		},
	)

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("ChatCompletion() error = nil, want adapter error")
	}
	if _, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("StreamChatCompletion() error = nil, want adapter error")
	}
	if upstreamCalled {
		t.Fatal("upstream called despite adapter error")
	}
}

func TestCompatibleProvider_ChatRequestHeaders_AppliedToChatOnly(t *testing.T) {
	headersByPath := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersByPath[r.URL.Path] = append(headersByPath[r.URL.Path], r.Header.Get("X-Conv-Id"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"resp","model":"m","choices":[]}`))
		}
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "affine",
			BaseURL:      server.URL,
			ChatRequestHeaders: func(_ context.Context, req *core.ChatRequest) http.Header {
				h := make(http.Header, 1)
				h.Set("X-Conv-Id", "conv-"+req.Model)
				return h
			},
		},
	)

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	_, _ = io.ReadAll(stream)
	stream.Close()
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	for _, got := range headersByPath["/chat/completions"] {
		if got != "conv-m" {
			t.Fatalf("chat X-Conv-Id = %q, want conv-m", got)
		}
	}
	if len(headersByPath["/chat/completions"]) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(headersByPath["/chat/completions"]))
	}
	for _, got := range headersByPath["/models"] {
		if got != "" {
			t.Fatalf("models X-Conv-Id = %q, want empty", got)
		}
	}
}

func TestCompatibleProvider_CreateBatch_InlineRequests(t *testing.T) {
	inlineReq := &core.BatchRequest{
		Endpoint:         "/v1/chat/completions",
		CompletionWindow: "24h",
		Requests: []core.BatchRequestItem{
			{CustomID: "a", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"gpt-4o-mini","messages":[]}`)},
			{CustomID: "b", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"gpt-4o-mini","messages":[]}`)},
		},
	}

	tests := []struct {
		name            string
		uploadStatus    int
		batchStatus     int
		wantErr         bool
		wantBatchCall   bool
		wantFileDeleted bool
	}{
		{name: "upload then create succeeds", uploadStatus: http.StatusOK, batchStatus: http.StatusOK, wantBatchCall: true},
		{name: "upload failure prevents batch create", uploadStatus: http.StatusInternalServerError, wantErr: true},
		{name: "create failure cleans up the uploaded file", uploadStatus: http.StatusOK, batchStatus: http.StatusBadRequest, wantErr: true, wantBatchCall: true, wantFileDeleted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var uploadedJSONL string
			var batchCreateBody map[string]any
			batchCalled := false
			fileDeleted := false

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/files" && r.Method == http.MethodPost:
					if tc.uploadStatus != http.StatusOK {
						w.WriteHeader(tc.uploadStatus)
						_, _ = w.Write([]byte(`{"error":{"message":"upload boom"}}`))
						return
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatalf("parse multipart: %v", err)
					}
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Fatalf("read file part: %v", err)
					}
					content, _ := io.ReadAll(file)
					uploadedJSONL = string(content)
					_, _ = w.Write([]byte(`{"id":"file-abc","object":"file","purpose":"batch"}`))
				case r.URL.Path == "/batches" && r.Method == http.MethodPost:
					batchCalled = true
					if tc.batchStatus != http.StatusOK {
						w.WriteHeader(tc.batchStatus)
						_, _ = w.Write([]byte(`{"error":{"message":"create boom"}}`))
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&batchCreateBody); err != nil {
						t.Fatalf("decode batch body: %v", err)
					}
					_, _ = w.Write([]byte(`{"id":"batch-up-1","object":"batch","status":"validating","input_file_id":"file-abc"}`))
				case r.URL.Path == "/files/file-abc" && r.Method == http.MethodDelete:
					fileDeleted = true
					_, _ = w.Write([]byte(`{"id":"file-abc","object":"file","deleted":true}`))
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()

			provider := NewCompatibleProviderWithHTTPClient(
				"test-key",
				server.Client(),
				llmclient.Hooks{},
				CompatibleProviderConfig{ProviderName: "openai", BaseURL: server.URL},
			)

			resp, err := provider.CreateBatch(context.Background(), inlineReq)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
			} else {
				if err != nil {
					t.Fatalf("CreateBatch: %v", err)
				}
				if resp.ID != "batch-up-1" {
					t.Fatalf("batch id = %q", resp.ID)
				}

				lines := strings.Split(strings.TrimSpace(uploadedJSONL), "\n")
				if len(lines) != 2 {
					t.Fatalf("uploaded JSONL lines = %d content=%q", len(lines), uploadedJSONL)
				}
				var first map[string]any
				if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
					t.Fatalf("first line: %v", err)
				}
				if first["custom_id"] != "a" || first["url"] != "/v1/chat/completions" || first["method"] != "POST" {
					t.Fatalf("first line = %v", first)
				}
				if batchCreateBody["input_file_id"] != "file-abc" {
					t.Fatalf("batch create input_file_id = %v", batchCreateBody["input_file_id"])
				}
				if _, hasInline := batchCreateBody["requests"]; hasInline {
					t.Fatalf("inline requests leaked to the provider create body: %v", batchCreateBody)
				}
			}
			if batchCalled != tc.wantBatchCall {
				t.Fatalf("batch endpoint called = %v, want %v", batchCalled, tc.wantBatchCall)
			}
			if fileDeleted != tc.wantFileDeleted {
				t.Fatalf("input file deleted = %v, want %v", fileDeleted, tc.wantFileDeleted)
			}
		})
	}
}
