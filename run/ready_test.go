package run

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func TestReadyProbeURL(t *testing.T) {
	tests := []struct {
		name     string
		server   config.ServerConfig
		expected string
	}{
		{
			name:     "default base path",
			server:   config.ServerConfig{Port: "8080", BasePath: "/"},
			expected: "http://127.0.0.1:8080/health/ready",
		},
		{
			name:     "prefixed base path",
			server:   config.ServerConfig{Port: "9090", BasePath: "/g"},
			expected: "http://127.0.0.1:9090/g/health/ready",
		},
		{
			name:     "empty port falls back to default",
			server:   config.ServerConfig{BasePath: "/"},
			expected: "http://127.0.0.1:8080/health/ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probeURL(tt.server, "/health/ready")
			if got != tt.expected {
				t.Fatalf("probeURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCheckReadyEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "ready",
			statusCode: http.StatusOK,
			body:       `{"status":"ready"}`,
		},
		{
			name:       "degraded still serviceable",
			statusCode: http.StatusOK,
			body:       `{"status":"degraded"}`,
		},
		{
			name:       "not ready",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"status":"not_ready"}`,
			wantErr:    "HTTP 503",
		},
		{
			name:       "bad response body",
			statusCode: http.StatusOK,
			body:       `not json`,
			wantErr:    "decode readiness response",
		},
		{
			name:       "unexpected status with 200",
			statusCode: http.StatusOK,
			body:       `{"status":"starting"}`,
			wantErr:    `readiness status is "starting"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			err := checkReadyEndpoint(context.Background(), server.Client(), server.URL)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkReadyEndpoint() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkReadyEndpoint() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
