package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/require"
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
		require.Error(t, err)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, core.ErrorTypeProvider, gwErr.Type)
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
				require.Error(t, err)
				_, ok := errors.AsType[*core.GatewayError](err)
				require.True(t, ok)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantID, got)
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
				require.NoError(t, err)
				require.Equal(t, "/files/file_123", gotPath)
				require.NotNil(t, fileObj)
				require.Equal(t, "file_123", fileObj.ID)
				require.Equal(t, "file", fileObj.Object)

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
				require.NoError(t, err)
				require.Equal(t, "/files/file_456", gotPath)
				require.NotNil(t, deleteResp)
				require.Equal(t, "file_456", deleteResp.ID)
				require.Equal(t, "file.deleted", deleteResp.Object)

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
				require.Equal(t, "/files/file_789", gotPath)
				require.Error(t, err)

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
			require.Equal(t, tt.method, gotMethod)
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
				require.Error(t, err)
				_, ok := errors.AsType[*core.GatewayError](err)
				require.True(t, ok)

				return
			}
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Equal(t, http.MethodGet, gotMethod)
			require.Equal(t, tt.wantPath, gotPath)
			require.Equal(t, "file_123", resp.ID)
			require.Equal(t, "file-bytes", string(resp.Data))
		})
	}
}
