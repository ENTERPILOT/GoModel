package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

type fakeProbe struct {
	err error
}

func (f fakeProbe) Ping(context.Context) error { return f.err }

type fakeInventory struct {
	models, providers int
}

func (f fakeInventory) ModelCount() int    { return f.models }
func (f fakeInventory) ProviderCount() int { return f.providers }

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
			name:           "models loaded",
			config:         &Config{StorageProbe: fakeProbe{}, ModelInventory: fakeInventory{models: 7, providers: 1}},
			wantStatusCode: http.StatusOK,
			wantStatus:     "ready",
			wantComponents: map[string]string{"storage": "ok", "models": "ok"},
		},
		{
			name:           "no models yet is degraded but stays in rotation",
			config:         &Config{StorageProbe: fakeProbe{}, ModelInventory: fakeInventory{providers: 1}},
			wantStatusCode: http.StatusOK,
			wantStatus:     "degraded",
			wantComponents: map[string]string{"storage": "ok", "models": "down"},
		},
		{
			name:           "no providers configured reports no models component",
			config:         &Config{ModelInventory: fakeInventory{}},
			wantStatusCode: http.StatusOK,
			wantStatus:     "ready",
			wantComponents: map[string]string{},
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

			require.Equal(t, tt.wantStatusCode, rec.Code, rec.Body.String())

			body := echotest.Decode[readinessResponse](t, rec)
			assert.Equal(t, tt.wantStatus, body.Status)

			for comp, want := range tt.wantComponents {
				got := body.Components[comp]
				assert.Equal(t, want, got)
			}
			assert.Len(t, body.Components, len(tt.wantComponents))
		})
	}
}

func TestReadyEndpointSkipsAuth(t *testing.T) {
	srv := New(&mockProvider{}, &Config{MasterKey: "secret", StorageProbe: fakeProbe{}})

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
