package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// A managed key with dashboard access, authenticated with its own token, must
// not lift its own restrictions or issue keys broader than itself. The admin
// handlers enforce this from the credential the auth middleware records, so
// this runs the whole server rather than a handler with a prepared context.
func TestManagedDashboardKeyCannotWidenItsOwnAccess(t *testing.T) {
	store, err := authkeys.NewSQLStore(t.Context(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	service, err := authkeys.NewService(store)
	require.NoError(t, err)
	lead, err := service.Create(t.Context(), authkeys.CreateInput{
		Name:            "lead",
		UserPath:        "/team/a",
		Labels:          []string{"team-a"},
		AllowedModels:   []string{"openai/gpt-4o-mini"},
		DashboardAccess: true,
	})
	require.NoError(t, err)

	srv := New(&mockProvider{}, &Config{
		MasterKey:             "master",
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil, admin.WithAuthKeys(service)),
		Authenticator:         service,
	})
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+lead.Value)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
		code   string
	}{
		{name: "clear its own allowlist", method: http.MethodPut, path: "/admin/auth-keys/" + lead.ID + "/allowed-models", body: `{"allowed_models":[]}`, want: http.StatusForbidden, code: "auth_key_self_update"},
		{name: "drop its own labels", method: http.MethodPut, path: "/admin/auth-keys/" + lead.ID + "/labels", body: `{"labels":[]}`, want: http.StatusForbidden, code: "auth_key_self_update"},
		{name: "rename itself", method: http.MethodPut, path: "/admin/auth-keys/" + lead.ID + "/name", body: `{"name":"renamed"}`, want: http.StatusForbidden, code: "auth_key_self_update"},
		{name: "issue an unrestricted key", method: http.MethodPost, path: "/admin/auth-keys", body: `{"name":"child","user_path":"/team/a/svc"}`, want: http.StatusForbidden, code: "allowed_models_out_of_scope"},
		{name: "issue a key for another model", method: http.MethodPost, path: "/admin/auth-keys", body: `{"name":"child","user_path":"/team/a/svc","allowed_models":["openai/gpt-4o"]}`, want: http.StatusForbidden, code: "allowed_models_out_of_scope"},
		{name: "issue a key within its own models", method: http.MethodPost, path: "/admin/auth-keys", body: `{"name":"child","user_path":"/team/a/svc","allowed_models":["openai/gpt-4o-mini"]}`, want: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := send(tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.code != "" {
				assert.Contains(t, rec.Body.String(), tt.code)
			}
		})
	}

	view, err := service.View(lead.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"openai/gpt-4o-mini"}, view.AllowedModels)
	assert.Equal(t, []string{"team-a"}, view.Labels)
}

// The response to POST /admin/auth-keys is the only place the plaintext token
// of a new key appears. The audit log captures admin response bodies when
// LOGGING_ONLY_MODEL_INTERACTIONS is off, and must not keep the token.
func TestIssuedAuthKeyIsNotStoredInAuditLog(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		accept string
	}{
		{name: "admin route", path: "/admin/auth-keys"},
		{name: "gzip response", path: "/admin/auth-keys", accept: "gzip"},
		{name: "legacy alias", path: "/admin/api/v1/auth-keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := authkeys.NewSQLStore(t.Context(), sqlxtest.NewSQLite(t))
			require.NoError(t, err)
			service, err := authkeys.NewService(store)
			require.NoError(t, err)
			logger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
			srv := New(&mockProvider{}, &Config{
				MasterKey:             "master",
				AdminEndpointsEnabled: true,
				AdminHandler:          admin.NewHandler(nil, nil, admin.WithAuthKeys(service)),
				Authenticator:         service,
				AuditLogger:           logger,
			})

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"name":"ci"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer master")
			if tt.accept != "" {
				req.Header.Set("Accept-Encoding", tt.accept)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			if tt.accept == "" {
				assert.Contains(t, rec.Body.String(), `"value":"`+authkeys.DefaultTokenPrefix, "the caller still receives the token")
			}

			keys := service.ListViews()
			require.Len(t, keys, 1)
			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].Data)
			logged, ok := auditlog.BodyDocument(logger.entries[0].Data.ResponseBody).(map[string]any)
			require.True(t, ok, "response body: %v", logger.entries[0].Data.ResponseBody)
			assert.Equal(t, "[REDACTED]", logged["value"])
			assert.Equal(t, keys[0].ID, logged["id"])
			assert.Equal(t, "ci", logged["name"])
		})
	}
}
