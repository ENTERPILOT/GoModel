package azure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestChatCompletion_UsesAzureAuthAndDefaultAPIVersion(t *testing.T) {
	var gotPath string
	var gotAPIVersion string
	var gotAPIKey string
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIVersion = r.URL.Query().Get("api-version")
		gotAPIKey = r.Header.Get("api-key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hello"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-4o",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAPIKey != "test-api-key" {
		t.Fatalf("api-key = %q, want test-api-key", gotAPIKey)
	}
	if gotAuthorization != "" {
		t.Fatalf("authorization = %q, want empty", gotAuthorization)
	}
	if gotAPIVersion != defaultAPIVersion {
		t.Fatalf("api-version = %q, want %q", gotAPIVersion, defaultAPIVersion)
	}
}

func TestSetAPIVersion_OverridesDefault(t *testing.T) {
	var gotAPIVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIVersion = r.URL.Query().Get("api-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hello"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.SetAPIVersion("2025-04-01-preview")

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-4o",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAPIVersion != "2025-04-01-preview" {
		t.Fatalf("api-version = %q, want 2025-04-01-preview", gotAPIVersion)
	}
}

func TestListModels_UsesAzureOpenAIPath(t *testing.T) {
	var gotPath string
	var gotAPIVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIVersion = r.URL.Query().Get("api-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/openai/models" {
		t.Fatalf("path = %q, want /openai/models", gotPath)
	}
	if gotAPIVersion != defaultAPIVersion {
		t.Fatalf("api-version = %q, want %q", gotAPIVersion, defaultAPIVersion)
	}
}

func TestBatchEndpoints_UseAzureOpenAIPaths(t *testing.T) {
	tests := []struct {
		name         string
		call         func(*Provider) error
		wantPath     string
		wantMethod   string
		responseBody string
	}{
		{
			name: "create",
			call: func(p *Provider) error {
				_, err := p.CreateBatch(context.Background(), &core.BatchRequest{
					InputFileID:      "file-123",
					Endpoint:         "/v1/chat/completions",
					CompletionWindow: "24h",
				})
				return err
			},
			wantPath:   "/openai/batches",
			wantMethod: http.MethodPost,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"validating",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
		{
			name: "get",
			call: func(p *Provider) error {
				_, err := p.GetBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:   "/openai/batches/batch_123",
			wantMethod: http.MethodGet,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"validating",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
		{
			name: "list",
			call: func(p *Provider) error {
				_, err := p.ListBatches(context.Background(), 10, "batch_122")
				return err
			},
			wantPath:   "/openai/batches",
			wantMethod: http.MethodGet,
			responseBody: `{
				"object":"list",
				"data":[],
				"has_more":false
			}`,
		},
		{
			name: "cancel",
			call: func(p *Provider) error {
				_, err := p.CancelBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:   "/openai/batches/batch_123/cancel",
			wantMethod: http.MethodPost,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"cancelling",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotMethod string
			var gotAPIVersion string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				gotAPIVersion = r.URL.Query().Get("api-version")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			if err := tt.call(provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotMethod != tt.wantMethod {
				t.Fatalf("method = %q, want %q", gotMethod, tt.wantMethod)
			}
			if gotAPIVersion != defaultAPIVersion {
				t.Fatalf("api-version = %q, want %q", gotAPIVersion, defaultAPIVersion)
			}
		})
	}
}

func TestListModels_UsesAzureResourceRootForDeploymentScopedBaseURL(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/openai/deployments/gpt-4o")

	_, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/openai/models" {
		t.Fatalf("path = %q, want /openai/models", gotPath)
	}
}

func TestBatchEndpoints_UseAzureResourceRootForDeploymentScopedBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		call         func(*Provider) error
		wantPath     string
		wantMethod   string
		responseBody string
	}{
		{
			name: "create",
			call: func(p *Provider) error {
				_, err := p.CreateBatch(context.Background(), &core.BatchRequest{
					InputFileID:      "file-123",
					Endpoint:         "/v1/chat/completions",
					CompletionWindow: "24h",
				})
				return err
			},
			wantPath:   "/openai/batches",
			wantMethod: http.MethodPost,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"validating",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
		{
			name: "get",
			call: func(p *Provider) error {
				_, err := p.GetBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:   "/openai/batches/batch_123",
			wantMethod: http.MethodGet,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"validating",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
		{
			name: "list",
			call: func(p *Provider) error {
				_, err := p.ListBatches(context.Background(), 10, "batch_122")
				return err
			},
			wantPath:   "/openai/batches",
			wantMethod: http.MethodGet,
			responseBody: `{
				"object":"list",
				"data":[],
				"has_more":false
			}`,
		},
		{
			name: "cancel",
			call: func(p *Provider) error {
				_, err := p.CancelBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:   "/openai/batches/batch_123/cancel",
			wantMethod: http.MethodPost,
			responseBody: `{
				"id":"batch_123",
				"object":"batch",
				"endpoint":"/v1/chat/completions",
				"status":"cancelling",
				"created_at":1677652288,
				"request_counts":{"total":1,"completed":0,"failed":0}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotMethod string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
			provider.SetBaseURL(server.URL + "/openai/deployments/gpt-4o")

			if err := tt.call(provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotMethod != tt.wantMethod {
				t.Fatalf("method = %q, want %q", gotMethod, tt.wantMethod)
			}
		})
	}
}
