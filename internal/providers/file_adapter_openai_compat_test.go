package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func newOpenAICompatibleTestClient(server *httptest.Server) *llmclient.Client {
	cfg := llmclient.DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 0
	return llmclient.NewWithHTTPClient(server.Client(), cfg, nil)
}

func TestValidatedOpenAICompatibleFileID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	client := newOpenAICompatibleTestClient(server)

	t.Run("nil client is provider error", func(t *testing.T) {
		_, err := validatedOpenAICompatibleFileID(nil, "file_123")
		if err == nil {
			t.Fatal("expected error")
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.Type != core.ErrorTypeProvider {
			t.Fatalf("error type = %s, want %s", gwErr.Type, core.ErrorTypeProvider)
		}
	})

	tests := []struct {
		name    string
		id      string
		wantID  string
		wantErr bool
	}{
		{name: "surrounding whitespace is trimmed", id: "  file_123  ", wantID: "file_123"},
		{name: "whitespace only is rejected", id: "   \t\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatedOpenAICompatibleFileID(client, tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				var gwErr *core.GatewayError
				if !errors.As(err, &gwErr) {
					t.Fatalf("expected GatewayError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantID {
				t.Fatalf("validated id = %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestDoOpenAICompatibleFileIDRequest(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		id            string
		statusCode    int
		responseBody  string
		defaultObject string
		check         func(t *testing.T, gotPath string, fileObj *core.FileObject, deleteResp *core.FileDeleteResponse, err error)
	}{
		{
			name:          "file object trims request id and synthesizes object",
			method:        http.MethodGet,
			id:            "  file_123  ",
			statusCode:    http.StatusOK,
			responseBody:  `{"filename":"a.jsonl","purpose":"batch"}`,
			defaultObject: "file",
			check: func(t *testing.T, gotPath string, fileObj *core.FileObject, _ *core.FileDeleteResponse, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotPath != "/files/file_123" {
					t.Fatalf("path = %q, want /files/file_123", gotPath)
				}
				if fileObj == nil {
					t.Fatal("expected file object")
				}
				if fileObj.ID != "file_123" {
					t.Fatalf("ID = %q, want file_123", fileObj.ID)
				}
				if fileObj.Object != "file" {
					t.Fatalf("Object = %q, want file", fileObj.Object)
				}
			},
		},
		{
			name:          "delete response trims request id and synthesizes object",
			method:        http.MethodDelete,
			id:            "  file_456  ",
			statusCode:    http.StatusOK,
			responseBody:  `{"deleted":true}`,
			defaultObject: "file.deleted",
			check: func(t *testing.T, gotPath string, _ *core.FileObject, deleteResp *core.FileDeleteResponse, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotPath != "/files/file_456" {
					t.Fatalf("path = %q, want /files/file_456", gotPath)
				}
				if deleteResp == nil {
					t.Fatal("expected delete response")
				}
				if deleteResp.ID != "file_456" {
					t.Fatalf("ID = %q, want file_456", deleteResp.ID)
				}
				if deleteResp.Object != "file.deleted" {
					t.Fatalf("Object = %q, want file.deleted", deleteResp.Object)
				}
			},
		},
		{
			name:          "upstream error is propagated",
			method:        http.MethodGet,
			id:            "file_789",
			statusCode:    http.StatusBadGateway,
			responseBody:  `{"error":{"message":"upstream failure"}}`,
			defaultObject: "file",
			check: func(t *testing.T, gotPath string, _ *core.FileObject, _ *core.FileDeleteResponse, err error) {
				t.Helper()
				if gotPath != "/files/file_789" {
					t.Fatalf("path = %q, want /files/file_789", gotPath)
				}
				if err == nil {
					t.Fatal("expected error")
				}
			},
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
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			client := newOpenAICompatibleTestClient(server)

			switch tt.method {
			case http.MethodDelete:
				resp, err := doOpenAICompatibleFileIDRequestWithPreparer[core.FileDeleteResponse](context.Background(), client, tt.method, tt.id, tt.defaultObject, nil)
				tt.check(t, gotPath, nil, resp, err)
			default:
				resp, err := doOpenAICompatibleFileIDRequestWithPreparer[core.FileObject](context.Background(), client, tt.method, tt.id, tt.defaultObject, nil)
				tt.check(t, gotPath, resp, nil, err)
			}
			if gotMethod != tt.method {
				t.Fatalf("method = %q, want %q", gotMethod, tt.method)
			}
		})
	}
}

func TestGetOpenAICompatibleFileContent(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantPath string
		wantErr  bool
	}{
		{name: "trimmed id uses normalized content endpoint", id: "  file_123  ", wantPath: "/files/file_123/content"},
		{name: "whitespace only is rejected", id: "   \n\t", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotMethod string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("file-bytes"))
			}))
			defer server.Close()

			client := newOpenAICompatibleTestClient(server)
			resp, err := GetOpenAICompatibleFileContent(context.Background(), client, tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				var gwErr *core.GatewayError
				if !errors.As(err, &gwErr) {
					t.Fatalf("expected GatewayError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp == nil {
				t.Fatal("expected response")
			}
			if gotMethod != http.MethodGet {
				t.Fatalf("method = %q, want GET", gotMethod)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if resp.ID != "file_123" {
				t.Fatalf("ID = %q, want file_123", resp.ID)
			}
			if string(resp.Data) != "file-bytes" {
				t.Fatalf("body = %q, want file-bytes", string(resp.Data))
			}
		})
	}
}
