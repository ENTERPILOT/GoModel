//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// failingModelHandler makes the mock answer 503 for one model and serve the
// rest normally, so a redirect's first target is down while its fallback is up.
// The mock has already consumed the body when the handler runs, so the model
// is read from the request it just recorded.
func failingModelHandler(model string) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, _ *http.Request) bool {
		recorded := mockServer.Requests()
		if len(recorded) == 0 {
			return false
		}
		var req struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(recorded[len(recorded)-1].Body, &req) != nil || strings.TrimPrefix(req.Model, "test/") != model {
			return false
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"` + model + ` is down","type":"server_error"}}`))
		return true
	}
}

func adminJSON(t *testing.T, method, url string, payload any) *http.Response {
	t.Helper()
	return sendBudgetJSONRequestWithHeaders(t, method, url, payload, map[string]string{
		"Authorization": "Bearer " + testMasterKey,
	})
}

// The per-redirect failover flag, wired exactly like the app: the virtual
// models service is both the model resolver and the failover resolver. A
// redirect fails over by default, stops when the flag is off, and the
// failover strategy ignores the flag. The flag round-trips the admin API.
func TestVirtualModelFailoverFlag_E2E(t *testing.T) {
	mockServer.ResetRequests()
	mockServer.SetCustomHandler(failingModelHandler("gpt-4"))
	t.Cleanup(func() { mockServer.SetCustomHandler(nil) })

	registry := setupE2ERegistry(t, "")
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	vmDB, err := sqlx.NewSQLite(db)
	require.NoError(t, err)
	vmStore, err := virtualmodels.NewSQLStore(t.Context(), vmDB)
	require.NoError(t, err)
	vmService, err := virtualmodels.NewService(vmStore, registry, true)
	require.NoError(t, err)

	ts := setupE2EAdminServer(t, e2eServerOptions{
		masterKey:        testMasterKey,
		registry:         registry,
		modelResolver:    vmService,
		failoverResolver: vmService,
		adminOptions:     []admin.Option{admin.WithVirtualModels(vmService)},
	})
	defer ts.Close()

	targets := []map[string]any{{"model": "test/gpt-4"}, {"model": "test/gpt-3.5-turbo"}}
	upsert := func(body map[string]any) {
		t.Helper()
		resp := adminJSON(t, http.MethodPut, ts.URL+"/admin/virtual-models", body)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "upsert %v", body["source"])
	}
	upsert(map[string]any{"source": "resilient", "strategy": "round_robin", "targets": targets})
	upsert(map[string]any{"source": "balanced-only", "strategy": "round_robin", "targets": targets, "failover": false})
	upsert(map[string]any{"source": "priority", "strategy": "failover", "targets": targets, "failover": false})

	// The admin API reports the opt-out and omits the default.
	list := adminJSON(t, http.MethodGet, ts.URL+"/admin/virtual-models", nil)
	var views []struct {
		Source   string `json:"source"`
		Failover *bool  `json:"failover"`
	}
	require.NoError(t, json.NewDecoder(list.Body).Decode(&views))
	closeBody(list)
	flags := map[string]*bool{}
	for _, view := range views {
		flags[view.Source] = view.Failover
	}
	require.Nil(t, flags["resilient"], "default must not be materialised")
	require.NotNil(t, flags["balanced-only"])
	require.False(t, *flags["balanced-only"])

	cases := []struct {
		source     string
		wantStatus int
		wantModels []string
	}{
		// Default: the failed primary is retried on the fallback.
		{"resilient", http.StatusOK, []string{"gpt-4", "gpt-3.5-turbo"}},
		// Opted out: the primary's error is returned, nothing else is tried.
		{"balanced-only", http.StatusServiceUnavailable, []string{"gpt-4"}},
		// Failover strategy: a priority list always cascades.
		{"priority", http.StatusOK, []string{"gpt-4", "gpt-3.5-turbo"}},
	}
	for _, tc := range cases {
		mockServer.ResetRequests()
		resp := sendBudgetJSONRequestWithHeaders(t, http.MethodPost, ts.URL+chatCompletionsPath, core.ChatRequest{
			Model:    tc.source,
			Messages: []core.Message{{Role: "user", Content: "hello"}},
		}, map[string]string{"Authorization": "Bearer " + testMasterKey, "X-Request-ID": "flag-" + tc.source})
		require.Equal(t, tc.wantStatus, resp.StatusCode, tc.source)
		if tc.wantStatus == http.StatusOK {
			var chat core.ChatResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&chat))
			require.NotEmpty(t, chat.Choices, tc.source)
		}
		closeBody(resp)
		require.Equal(t, tc.wantModels, recordedChatModels(t), tc.source)
	}
}
