package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gomodel/config"
)

func TestHealthProbeURL(t *testing.T) {
	tests := []struct {
		name     string
		server   config.ServerConfig
		expected string
	}{
		{
			name:     "default base path",
			server:   config.ServerConfig{Port: "8080", BasePath: "/"},
			expected: "http://127.0.0.1:8080/health",
		},
		{
			name:     "prefixed base path",
			server:   config.ServerConfig{Port: "9090", BasePath: "/g"},
			expected: "http://127.0.0.1:9090/g/health",
		},
		{
			name:     "empty port falls back to default",
			server:   config.ServerConfig{BasePath: "/"},
			expected: "http://127.0.0.1:8080/health",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := healthProbeURL(tt.server)
			if got != tt.expected {
				t.Fatalf("healthProbeURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCheckHealthEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "healthy",
			statusCode: http.StatusOK,
			body:       `{"status":"ok"}`,
		},
		{
			name:       "bad status code",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"status":"ok"}`,
			wantErr:    "HTTP 503",
		},
		{
			name:       "bad response body",
			statusCode: http.StatusOK,
			body:       `not json`,
			wantErr:    "decode health response",
		},
		{
			name:       "unhealthy response status",
			statusCode: http.StatusOK,
			body:       `{"status":"starting"}`,
			wantErr:    `health status is "starting"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			err := checkHealthEndpoint(context.Background(), server.Client(), server.URL)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkHealthEndpoint() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkHealthEndpoint() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunHealthProbe_UsesConfiguredPortAndBasePath(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/g/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	t.Setenv("PORT", port)
	t.Setenv("BASE_PATH", "/g")

	if err := runHealthProbe(time.Second); err != nil {
		t.Fatalf("runHealthProbe() error = %v, want nil", err)
	}
}
