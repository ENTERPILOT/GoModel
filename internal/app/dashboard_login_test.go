package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/dashboardlogin"
	"github.com/enterpilot/gomodel/internal/providers"
)

// DASHBOARD_PASSWORD alone puts the admin API behind the dashboard login, on a
// gateway that has no master key and would otherwise leave it open.
func TestDashboardPasswordGuardsTheAdminAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "login.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("GOMODEL_MASTER_KEY", "")
	t.Setenv("DASHBOARD_PASSWORD", "s3cret")
	loaded, err := config.Load()
	require.NoError(t, err)

	ctx := context.Background()
	application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, application.Shutdown(ctx)) })

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		application.server.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusUnauthorized, serve(httptest.NewRequest(http.MethodGet, "/admin/models", nil)).Code)

	signIn := httptest.NewRequest(http.MethodPost, dashboardlogin.SessionPath, strings.NewReader(`{"username":"admin","password":"s3cret"}`))
	signIn.Header.Set("Content-Type", "application/json")
	signedIn := serve(signIn)
	require.Equal(t, http.StatusOK, signedIn.Code, signedIn.Body.String())
	cookies := signedIn.Result().Cookies()
	require.Len(t, cookies, 1)

	withSession := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	withSession.AddCookie(cookies[0])
	assert.Equal(t, http.StatusOK, serve(withSession).Code)
}
