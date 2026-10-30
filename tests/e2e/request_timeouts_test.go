//go:build e2e

package e2e

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/stretchr/testify/require"
)

func TestRequestTimeoutOverridesAcrossFailover_E2E(t *testing.T) {
	t.Setenv("HTTP_TIMEOUT", "40ms")
	t.Setenv("HTTP_RESPONSE_HEADER_TIMEOUT", "40ms")
	registry := providers.NewModelRegistry()
	factory := providers.NewProviderFactory()
	factory.Add(openai.Registration)
	captures := []*providertest.Capture{}
	for _, name := range []string{"primary", "backup"} {
		upstream, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
			"/models": func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"model","object":"model"}]}`)
			},
			"/chat/completions": func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(180 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if name == "primary" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"ok","model":"model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			},
		})
		captures = append(captures, capture)
		provider, err := factory.Create(providers.ProviderConfig{
			Name: name, Type: "openai", BaseURL: upstream.URL, APIKey: "local-test-key",
		})
		require.NoError(t, err)
		registry.RegisterProviderWithNameAndType(provider, name, "openai")
	}
	require.NoError(t, registry.Initialize(t.Context()))
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	connection, err := sqlx.NewSQLite(db)
	require.NoError(t, err)
	store, err := virtualmodels.NewSQLStore(t.Context(), connection)
	require.NoError(t, err)
	models, err := virtualmodels.NewService(store, registry, true)
	require.NoError(t, err)
	require.NoError(t, models.Upsert(t.Context(), virtualmodels.VirtualModel{
		Source: "pool", Enabled: true, Strategy: virtualmodels.StrategyFailover,
		Targets: []virtualmodels.Target{{Model: "primary/model"}, {Model: "backup/model"}},
	}))
	gateway := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		registry: registry, modelResolver: models, failoverResolver: models,
	}))
	t.Cleanup(gateway.Close)
	for _, tc := range []struct {
		name, override string
		want           int
	}{
		{"inherited", "", http.StatusGatewayTimeout},
		// Each attempt takes 180ms; the combined flow exceeds the 300ms override.
		{"fresh budget per fallback", "300", http.StatusOK},
		{"disabled", "0", http.StatusOK},
		{"short override", "10", http.StatusGatewayTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"pool","messages":[{"role":"user","content":"hi"}]}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			if tc.override != "" {
				req.Header.Set("X-GoModel-Request-Timeout-Ms", tc.override)
				req.Header.Set("X-GoModel-Response-Header-Timeout-Ms", tc.override)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.want, resp.StatusCode, string(body))
		})
	}
	for _, capture := range captures {
		for _, sent := range capture.All() {
			require.Empty(t, sent.Header.Get("X-GoModel-Request-Timeout-Ms"))
			require.Empty(t, sent.Header.Get("X-GoModel-Response-Header-Timeout-Ms"))
		}
	}
}
