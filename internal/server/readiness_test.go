package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeProbe struct {
	err error
}

func (f fakeProbe) Ping(context.Context) error { return f.err }

func TestReadyEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		config         *Config
		wantStatusCode int
		wantStatus     string
		wantComponents map[string]string
	}{
		{
			name:           "no probes collapses to ready",
			config:         &Config{},
			wantStatusCode: http.StatusOK,
			wantStatus:     "ready",
			wantComponents: map[string]string{},
		},
		{
			name:           "storage ok",
			config:         &Config{StorageProbe: fakeProbe{}},
			wantStatusCode: http.StatusOK,
			wantStatus:     "ready",
			wantComponents: map[string]string{"storage": "ok"},
		},
		{
			name:           "storage down is not ready",
			config:         &Config{StorageProbe: fakeProbe{err: errors.New("boom")}},
			wantStatusCode: http.StatusServiceUnavailable,
			wantStatus:     "not_ready",
			wantComponents: map[string]string{"storage": "down"},
		},
		{
			name:           "cache down is degraded but still serving",
			config:         &Config{StorageProbe: fakeProbe{}, CacheProbe: fakeProbe{err: errors.New("boom")}},
			wantStatusCode: http.StatusOK,
			wantStatus:     "degraded",
			wantComponents: map[string]string{"storage": "ok", "cache": "down"},
		},
		{
			name:           "storage down dominates cache ok",
			config:         &Config{StorageProbe: fakeProbe{err: errors.New("boom")}, CacheProbe: fakeProbe{}},
			wantStatusCode: http.StatusServiceUnavailable,
			wantStatus:     "not_ready",
			wantComponents: map[string]string{"storage": "down", "cache": "ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&mockProvider{}, tt.config)

			req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatusCode {
				t.Fatalf("status code = %d, want %d (body: %s)", rec.Code, tt.wantStatusCode, rec.Body.String())
			}

			var body readinessResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, tt.wantStatus)
			}
			for comp, want := range tt.wantComponents {
				if got := body.Components[comp]; got != want {
					t.Errorf("component %q = %q, want %q", comp, got, want)
				}
			}
			if len(body.Components) != len(tt.wantComponents) {
				t.Errorf("components = %v, want %v", body.Components, tt.wantComponents)
			}
		})
	}
}

func TestReadyEndpointSkipsAuth(t *testing.T) {
	srv := New(&mockProvider{}, &Config{MasterKey: "secret", StorageProbe: fakeProbe{}})

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("readiness without auth = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}
