//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// A virtual model whose target an operator disabled is served by its next
// target instead of failing, through a chained virtual model too; with every
// target disabled it answers model_access_denied, and a direct request for
// the disabled model still fails. Wired like the app: the virtual models
// service resolves, fails over and authorizes.
func TestVirtualModelSkipsDisabledTarget_E2E(t *testing.T) {
	mockServer.ResetRequests()

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
		modelAuthorizer:  vmService,
		adminOptions:     []admin.Option{admin.WithVirtualModels(vmService)},
	})
	defer ts.Close()

	upsert := func(body map[string]any) {
		t.Helper()
		resp := adminJSON(t, http.MethodPut, ts.URL+"/admin/virtual-models", body)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "upsert %v", body["source"])
	}
	upsert(map[string]any{"source": "priority", "strategy": "failover",
		"targets": []map[string]any{{"model": "test/gpt-4"}, {"model": "test/gpt-3.5-turbo"}}})
	upsert(map[string]any{"source": "inner", "targets": []map[string]any{{"model": "test/gpt-4"}}})
	upsert(map[string]any{"source": "outer", "strategy": "failover",
		"targets": []map[string]any{{"model": "inner"}, {"model": "test/gpt-3.5-turbo"}}})
	upsert(map[string]any{"source": "test/gpt-4", "enabled": false})

	chat := func(model string) (int, string) {
		t.Helper()
		mockServer.ResetRequests()
		resp := sendBudgetJSONRequestWithHeaders(t, http.MethodPost, ts.URL+chatCompletionsPath, core.ChatRequest{
			Model:    model,
			Messages: []core.Message{{Role: "user", Content: "hello"}},
		}, map[string]string{"Authorization": "Bearer " + testMasterKey})
		defer closeBody(resp)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Error.Code
	}

	for _, source := range []string{"priority", "outer"} {
		status, _ := chat(source)
		require.Equal(t, http.StatusOK, status, source)
		assert.Equal(t, []string{"gpt-3.5-turbo"}, recordedChatModels(t), source)
	}

	status, code := chat("test/gpt-4")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "model_access_denied", code)
	assert.Empty(t, recordedChatModels(t))

	upsert(map[string]any{"source": "test/gpt-3.5-turbo", "enabled": false})
	status, code = chat("priority")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "model_access_denied", code)
	assert.Empty(t, recordedChatModels(t))
}
