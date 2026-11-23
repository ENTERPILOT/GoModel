//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// stalledEmbeddingUpstream lists model but holds every embeddings request
// until the client gives up, so only a timeout ends the attempt.
func stalledEmbeddingUpstream(t *testing.T, model string) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(core.ModelsResponse{
				Object: "list", Data: []core.Model{{ID: model, Object: "model", OwnedBy: "openai"}},
			})
		},
		"/embeddings": func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(30 * time.Second):
			}
		},
	})
}

// A virtual model's response_header_timeout through the real HTTP adapter,
// router and persisted virtual model: the stalled primary is abandoned after
// the virtual model's one-second budget, far below the gateway-wide 600s
// default, and the failover leg serves the request.
func TestVirtualModelTimeouts_E2E(t *testing.T) {
	primary, primaryCapture := stalledEmbeddingUpstream(t, "embedding-a")
	reserve, reserveCapture := embeddingFailoverUpstream(t, "embedding-a", http.StatusOK)
	factory := providers.NewProviderFactory()
	factory.Add(openai.Registration)
	registry := providers.NewModelRegistry()
	for _, target := range []struct{ name, url string }{
		{name: "timeout-primary", url: primary.URL},
		{name: "timeout-reserve", url: reserve.URL},
	} {
		provider, err := factory.Create(providers.ProviderConfig{
			Name: target.name, Type: "openai", BaseURL: target.url, APIKey: "local-test-key",
		})
		require.NoError(t, err)
		registry.RegisterProviderWithNameAndType(provider, target.name, "openai")
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
		Source: "quick-embeddings", Enabled: true, Strategy: virtualmodels.StrategyFailover,
		ResponseHeaderTimeout: new(1),
		Targets: []virtualmodels.Target{
			{Model: "timeout-primary/embedding-a"},
			{Model: "timeout-reserve/embedding-a"},
		},
	}))
	gateway := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		registry: registry, modelResolver: models, failoverResolver: models,
	}))
	t.Cleanup(gateway.Close)

	started := time.Now()
	response := sendJSONRequest(t, gateway.URL+"/v1/embeddings", map[string]any{
		"model": "quick-embeddings", "input": []string{"first", "second"},
	})
	defer response.Body.Close()
	elapsed := time.Since(started)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	assert.GreaterOrEqual(t, elapsed, time.Second)
	assert.Less(t, elapsed, 10*time.Second, "the stalled target is abandoned after the virtual model's budget")
	assert.Len(t, embeddingAttempts(primaryCapture), 1)
	assert.Len(t, embeddingAttempts(reserveCapture), 1)
}
