package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/dashboardlogin"
)

const (
	dashboardTestUsername = "admin"
	dashboardTestPassword = "correct horse battery staple"
)

func newDashboardLoginServer(t *testing.T, cfg *Config) *Server {
	t.Helper()
	service, err := dashboardlogin.New(dashboardTestUsername, dashboardTestPassword)
	require.NoError(t, err)
	cfg.DashboardLogin = dashboardlogin.NewHandler(service, "/")
	return New(&mockProvider{}, cfg)
}

// dashboardSignIn signs in through the session route and returns the cookie.
func dashboardSignIn(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	body := `{"username":"` + dashboardTestUsername + `","password":"` + dashboardTestPassword + `"}`
	req := httptest.NewRequest(http.MethodPost, dashboardlogin.SessionPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies[0]
}

type dashboardLoginRequest struct {
	name      string
	method    string // defaults to GET
	path      string
	session   bool
	token     string
	fetchSite string
	want      int
}

func runDashboardLoginRequests(t *testing.T, srv *Server, tests []dashboardLoginRequest) {
	t.Helper()
	session := dashboardSignIn(t, srv)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, tt.path, nil)
			if tt.session {
				req.AddCookie(session)
			}
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

// The dashboard login guards the admin API on its own: a gateway with no
// master key and no managed keys keeps serving model requests without a
// credential, as it did before the login was turned on.
func TestDashboardLoginGuardsAdminOnAnOpenGateway(t *testing.T) {
	srv := newDashboardLoginServer(t, &Config{
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
		AdminUIEnabled:        true,
		DashboardHandler:      newDashboardHandler(t),
	})

	runDashboardLoginRequests(t, srv, []dashboardLoginRequest{
		{name: "model API stays open", path: "/v1/models", want: http.StatusOK},
		{name: "admin API refuses a request without a session", path: "/admin/models", want: http.StatusUnauthorized},
		{name: "admin API serves a session", path: "/admin/models", session: true, want: http.StatusOK},
		{name: "gateway-wide admin route serves a session", path: "/admin/providers/status", session: true, want: http.StatusOK},
		{name: "legacy admin alias refuses a request without a session", path: "/admin/api/v1/models", want: http.StatusUnauthorized},
		{name: "legacy admin alias serves a session", path: "/admin/api/v1/models", session: true, want: http.StatusOK},
		{name: "an escaped admin path is not taken for a model route", path: "/%61dmin/models", want: http.StatusUnauthorized},
		{name: "a bearer token is not a credential here", path: "/admin/models", token: "anything", want: http.StatusUnauthorized},
		{name: "a bearer token outranks the session", path: "/admin/models", session: true, token: "anything", want: http.StatusUnauthorized},
		{name: "a session sent on behalf of another site is ignored", path: "/admin/models", session: true, fetchSite: "cross-site", want: http.StatusUnauthorized},
		{name: "a session sent by the dashboard itself is served", path: "/admin/models", session: true, fetchSite: "same-origin", want: http.StatusOK},
		{name: "the dashboard page needs no session", path: "/admin/dashboard", want: http.StatusOK},
		{name: "the session route needs no session", path: dashboardlogin.SessionPath, want: http.StatusOK},
		{name: "signing out needs no session", method: http.MethodDelete, path: dashboardlogin.SessionPath, want: http.StatusNoContent},
	})
}

func TestDashboardLoginAlongsideTheMasterKey(t *testing.T) {
	const masterKey = "test-secret-key"
	srv := newDashboardLoginServer(t, &Config{
		MasterKey:             masterKey,
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
	})

	runDashboardLoginRequests(t, srv, []dashboardLoginRequest{
		{name: "admin API refuses a request without a credential", path: "/admin/models", want: http.StatusUnauthorized},
		{name: "admin API serves a session", path: "/admin/models", session: true, want: http.StatusOK},
		{name: "admin API still serves the master key", path: "/admin/models", token: masterKey, want: http.StatusOK},
		{name: "model API refuses a request without a credential", path: "/v1/models", want: http.StatusUnauthorized},
		{name: "model API serves a session, for the playground", path: "/v1/models", session: true, want: http.StatusOK},
		{name: "model API still serves the master key", path: "/v1/models", token: masterKey, want: http.StatusOK},
		{name: "a wrong bearer token outranks the session", path: "/admin/models", session: true, token: "wrong", want: http.StatusUnauthorized},
	})
}

// With the master key turned off the gateway requires a credential everywhere;
// the dashboard login is then the way in to issue the first managed key.
func TestDashboardLoginWithMasterKeyDisabled(t *testing.T) {
	srv := newDashboardLoginServer(t, &Config{
		MasterKeyDisabled:     true,
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
	})

	runDashboardLoginRequests(t, srv, []dashboardLoginRequest{
		{name: "model API refuses a request without a credential", path: "/v1/models", want: http.StatusUnauthorized},
		{name: "admin API refuses a request without a credential", path: "/admin/models", want: http.StatusUnauthorized},
		{name: "admin API serves a session", path: "/admin/models", session: true, want: http.StatusOK},
		{name: "model API serves a session", path: "/v1/models", session: true, want: http.StatusOK},
	})
}

func TestDashboardLoginRejectsWrongCredentials(t *testing.T) {
	srv := newDashboardLoginServer(t, &Config{
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
	})

	req := httptest.NewRequest(http.MethodPost, dashboardlogin.SessionPath, strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, rec.Result().Cookies())
}

// The login guards the admin API, so without one there is nothing to sign in to.
func TestDashboardLoginNeedsTheAdminAPI(t *testing.T) {
	service, err := dashboardlogin.New(dashboardTestUsername, dashboardTestPassword)
	require.NoError(t, err)
	srv := New(&mockProvider{}, &Config{DashboardLogin: dashboardlogin.NewHandler(service, "/")})

	for path, want := range map[string]int{
		dashboardlogin.SessionPath: http.StatusNotFound,
		"/v1/models":               http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, want, rec.Code, path)
	}
}
