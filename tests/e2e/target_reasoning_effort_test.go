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

// recordedChatEfforts returns the reasoning effort each upstream chat request
// carried, in order, keyed "model=effort"; a request without one has an empty
// effort.
func recordedChatEfforts(t *testing.T) []string {
	t.Helper()
	efforts := make([]string, 0)
	for _, recorded := range mockServer.Requests() {
		var req struct {
			Model           string          `json:"model"`
			ReasoningEffort string          `json:"reasoning_effort"`
			Reasoning       *core.Reasoning `json:"reasoning"`
		}
		require.NoError(t, json.Unmarshal(recorded.Body, &req))
		effort := req.ReasoningEffort
		if effort == "" && req.Reasoning != nil {
			effort = req.Reasoning.Effort
		}
		efforts = append(efforts, strings.TrimPrefix(req.Model, "test/")+"="+effort)
	}
	return efforts
}

// A failover virtual model with a reasoning effort per target, wired exactly
// like the app: the first attempt and the failover leg each reach the upstream
// with the effort of the target they go to, whatever the client asked for, and
// a target that sets none keeps the virtual model's own effort. The per-target
// value round-trips the admin API.
func TestVirtualModelTargetReasoningEffort_E2E(t *testing.T) {
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

	upsert := func(body map[string]any) {
		t.Helper()
		resp := adminJSON(t, http.MethodPut, ts.URL+"/admin/virtual-models", body)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "upsert %v", body["source"])
	}
	upsert(map[string]any{"source": "per-target", "strategy": "failover", "targets": []map[string]any{
		{"model": "test/gpt-4", "reasoning_effort": "high"},
		{"model": "test/gpt-3.5-turbo", "reasoning_effort": "low"},
	}})
	upsert(map[string]any{"source": "mixed", "strategy": "failover", "reasoning_effort": "medium", "targets": []map[string]any{
		{"model": "test/gpt-4", "reasoning_effort": "high"},
		{"model": "test/gpt-3.5-turbo"},
	}})

	list := adminJSON(t, http.MethodGet, ts.URL+"/admin/virtual-models", nil)
	var views []virtualmodels.View
	require.NoError(t, json.NewDecoder(list.Body).Decode(&views))
	closeBody(list)
	stored := map[string][]string{}
	for _, view := range views {
		for _, target := range view.Targets {
			stored[view.Source] = append(stored[view.Source], target.ReasoningEffort)
		}
	}
	require.Equal(t, map[string][]string{"per-target": {"high", "low"}, "mixed": {"high", ""}}, stored)

	rejected := adminJSON(t, http.MethodPut, ts.URL+"/admin/virtual-models", map[string]any{
		"source": "bad", "targets": []map[string]any{{"model": "test/gpt-4", "reasoning_effort": "extreme"}},
	})
	require.Equal(t, http.StatusBadRequest, rejected.StatusCode)
	closeBody(rejected)

	cases := []struct {
		source string
		want   []string
	}{
		{"per-target", []string{"gpt-4=high", "gpt-3.5-turbo=low"}},
		{"mixed", []string{"gpt-4=high", "gpt-3.5-turbo=medium"}},
	}
	for _, tc := range cases {
		mockServer.ResetRequests()
		resp := sendBudgetJSONRequestWithHeaders(t, http.MethodPost, ts.URL+chatCompletionsPath, core.ChatRequest{
			Model:     tc.source,
			Messages:  []core.Message{{Role: "user", Content: "hello"}},
			Reasoning: &core.Reasoning{Effort: "minimal"},
		}, map[string]string{"Authorization": "Bearer " + testMasterKey, "X-Request-ID": "effort-" + tc.source})
		require.Equal(t, http.StatusOK, resp.StatusCode, tc.source)
		closeBody(resp)
		require.Equal(t, tc.want, recordedChatEfforts(t), tc.source)
	}
}
