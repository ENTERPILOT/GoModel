//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/stretchr/testify/require"
)

// Exercise the real HTTP adapter, router and persisted virtual-model chain,
// rather than supplying a failover resolver or embeddings provider stub.
func TestEmbeddingsVirtualModelFailover_E2E(t *testing.T) {
	for _, tc := range []struct {
		name          string
		primaryStatus int
		wantStatus    int
		wantReserve   int
	}{
		{name: "unavailable primary", primaryStatus: http.StatusServiceUnavailable, wantStatus: http.StatusOK, wantReserve: 1},
		{name: "invalid request", primaryStatus: http.StatusBadRequest, wantStatus: http.StatusBadRequest},
		{name: "invalid credential", primaryStatus: http.StatusUnauthorized, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, primaryCapture := embeddingFailoverUpstream(t, "embedding-a", tc.primaryStatus)
			reserve, reserveCapture := embeddingFailoverUpstream(t, "embedding-b", http.StatusOK)
			factory := providers.NewProviderFactory()
			factory.Add(openai.Registration)
			registry := providers.NewModelRegistry()
			for _, target := range []struct{ name, url string }{
				{name: "embedding-primary", url: primary.URL},
				{name: "embedding-reserve", url: reserve.URL},
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
				Source: "embedding-pool", Enabled: true, Strategy: virtualmodels.StrategyFailover,
				Targets: []virtualmodels.Target{
					{Model: "embedding-primary/embedding-a"},
					{Model: "embedding-reserve/embedding-b"},
				},
			}))
			gateway := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
				registry: registry, modelResolver: models, failoverResolver: models,
			}))
			t.Cleanup(gateway.Close)

			response := sendJSONRequest(t, gateway.URL+"/v1/embeddings", map[string]any{
				"model": "embedding-pool", "input": []string{"first", "second"},
				"dimensions": 2, "encoding_format": "base64", "user": "local-embedding-test",
			})
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, response.StatusCode, string(body))
			require.Len(t, embeddingAttempts(primaryCapture), 1)
			reserveAttempts := embeddingAttempts(reserveCapture)
			require.Len(t, reserveAttempts, tc.wantReserve)
			if tc.wantReserve == 0 {
				return
			}

			require.Equal(t, map[string]any{
				"model": "embedding-b", "input": []any{"first", "second"},
				"dimensions": float64(2), "encoding_format": "base64", "user": "local-embedding-test",
			}, reserveAttempts[0].JSON(t))
			var result core.EmbeddingResponse
			require.NoError(t, json.Unmarshal(body, &result))
			require.Equal(t, "embedding-b", result.Model)
			require.Len(t, result.Data, 2)
			for i, item := range result.Data {
				require.Equal(t, i, item.Index)
				var encoded string
				require.NoError(t, json.Unmarshal(item.Embedding, &encoded))
				require.NotEmpty(t, encoded)
			}
		})
	}
}

func embeddingFailoverUpstream(t *testing.T, model string, status int) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(core.ModelsResponse{
				Object: "list", Data: []core.Model{{ID: model, Object: "model", OwnedBy: "openai"}},
			})
		},
		"/embeddings": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if status != http.StatusOK {
				_, _ = io.WriteString(w, `{"error":{"message":"synthetic upstream failure","type":"upstream_error"}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(core.EmbeddingResponse{
				Object: "list", Model: model,
				Data: []core.EmbeddingData{
					{Object: "embedding", Index: 0, Embedding: json.RawMessage(`[0.1,0.2]`)},
					{Object: "embedding", Index: 1, Embedding: json.RawMessage(`[0.3,0.4]`)},
				},
				Usage: core.EmbeddingUsage{PromptTokens: 2, TotalTokens: 2},
			})
		},
	})
}

func embeddingAttempts(capture *providertest.Capture) []providertest.Recorded {
	var attempts []providertest.Recorded
	for _, request := range capture.All() {
		if request.Path == "/embeddings" {
			attempts = append(attempts, request)
		}
	}
	return attempts
}
