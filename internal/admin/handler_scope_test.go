package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/usage"
)

const scopeAlpha = "/team/alpha"

// scopedRequest builds a request whose credential is confined to userPath;
// an empty userPath yields a global credential.
func scopedRequest(method, target, body, userPath string) (*echo.Context, *httptest.ResponseRecorder) {
	c, rec := jsonRequest(method, target, body)
	if userPath != "" {
		req := c.Request()
		c.SetRequest(req.WithContext(core.WithAccessScope(req.Context(), core.AccessScope{UserPath: userPath})))
	}
	return c, rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

func TestRequireGlobalScope(t *testing.T) {
	h := &Handler{}
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	tests := []struct {
		name       string
		method     string
		path       string
		scope      string
		wantStatus int
		wantCode   string
	}{
		{name: "scoped credential denied on gateway-wide route", method: http.MethodGet, path: "/admin/providers/status", scope: scopeAlpha, wantStatus: http.StatusForbidden, wantCode: codeAdminScopeDenied},
		{name: "scoped credential denied on workflows", method: http.MethodGet, path: "/admin/workflows", scope: scopeAlpha, wantStatus: http.StatusForbidden, wantCode: codeAdminScopeDenied},
		{name: "scoped credential denied on reset all", method: http.MethodPost, path: "/admin/budgets/reset", scope: scopeAlpha, wantStatus: http.StatusForbidden, wantCode: codeAdminScopeDenied},
		{name: "scoped credential denied on usage throughput", method: http.MethodGet, path: "/admin/usage/throughput", scope: scopeAlpha, wantStatus: http.StatusForbidden, wantCode: codeAdminScopeDenied},
		// A zero-value handler answers 503 past the gate: the gate let it through.
		{name: "global credential passes gateway-wide route", method: http.MethodGet, path: "/admin/workflows", wantStatus: http.StatusServiceUnavailable},
		{name: "scoped credential reaches tenant route", method: http.MethodGet, path: "/admin/auth-keys", scope: scopeAlpha, wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.scope != "" {
				req = req.WithContext(core.WithAccessScope(req.Context(), core.AccessScope{UserPath: tt.scope}))
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantCode != "" {
				assert.Equal(t, tt.wantCode, errorCode(t, rec))
			}
		})
	}
}

func TestAccessEndpoint(t *testing.T) {
	h := NewHandler(nil, nil)

	c, rec := scopedRequest(http.MethodGet, "/admin/access", "", "")
	require.NoError(t, h.Access(c))
	assert.JSONEq(t, `{"scope":"global"}`, rec.Body.String())

	c, rec = scopedRequest(http.MethodGet, "/admin/access", "", scopeAlpha)
	require.NoError(t, h.Access(c))
	assert.JSONEq(t, `{"scope":"user_path","user_path":"/team/alpha"}`, rec.Body.String())
}

func TestScopedUserPathFilter(t *testing.T) {
	tests := []struct {
		name     string
		scope    string
		query    string
		wantPath string
		wantCode string
	}{
		{name: "global keeps empty", scope: "", query: "", wantPath: ""},
		{name: "global keeps any path", scope: "", query: "/team/beta", wantPath: "/team/beta"},
		{name: "scoped empty defaults to root", scope: scopeAlpha, query: "", wantPath: scopeAlpha},
		{name: "scoped root kept", scope: scopeAlpha, query: scopeAlpha, wantPath: scopeAlpha},
		{name: "scoped descendant kept", scope: scopeAlpha, query: "/team/alpha/svc", wantPath: "/team/alpha/svc"},
		{name: "scoped sibling rejected", scope: scopeAlpha, query: "/team/beta", wantCode: codeUserPathOutOfScope},
		{name: "scoped ancestor rejected", scope: scopeAlpha, query: "/team", wantCode: codeUserPathOutOfScope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageReader := &mockUsageReader{usageLog: &usage.UsageLogResult{}}
			auditReader := &mockAuditReader{logResult: &auditlog.LogListResult{}}
			h := NewHandler(usageReader, nil, WithAuditReader(auditReader))

			c, rec := scopedRequest(http.MethodGet, "/admin/usage/log?user_path="+tt.query, "", tt.scope)
			require.NoError(t, h.UsageLog(c))
			if tt.wantCode != "" {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Equal(t, tt.wantCode, errorCode(t, rec))
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, tt.wantPath, usageReader.lastUsageLog.UserPath)
			}

			c, rec = scopedRequest(http.MethodGet, "/admin/audit/log?user_path="+tt.query, "", tt.scope)
			require.NoError(t, h.AuditLog(c))
			if tt.wantCode != "" {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Equal(t, tt.wantCode, errorCode(t, rec))
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, tt.wantPath, auditReader.lastQuery.UserPath)
			}
		})
	}
}

func TestAuditDetailAndConversationHideOtherTenants(t *testing.T) {
	entry := func(id, userPath string) auditlog.LogEntry {
		return auditlog.LogEntry{ID: id, UserPath: userPath, Timestamp: time.Now(), Data: &auditlog.LogData{}}
	}
	tests := []struct {
		name       string
		scope      string
		entryPath  string
		wantStatus int
	}{
		{name: "global sees any entry", scope: "", entryPath: "/team/beta", wantStatus: http.StatusOK},
		{name: "scoped sees own entry", scope: scopeAlpha, entryPath: "/team/alpha/svc", wantStatus: http.StatusOK},
		{name: "scoped sees other tenant as missing", scope: scopeAlpha, entryPath: "/team/beta", wantStatus: http.StatusNotFound},
		{name: "scoped sees legacy empty path as missing", scope: scopeAlpha, entryPath: "", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchor := entry("log-1", tt.entryPath)
			reader := &mockAuditReader{
				logByID: &anchor,
				conversationResult: &auditlog.ConversationResult{
					AnchorID: "log-1",
					Entries:  []auditlog.LogEntry{anchor, entry("log-2", "/team/beta"), entry("log-3", "/team/alpha")},
				},
			}
			h := NewHandler(nil, nil, WithAuditReader(reader))

			c, rec := scopedRequest(http.MethodGet, "/admin/audit/detail?log_id=log-1", "", tt.scope)
			require.NoError(t, h.AuditLogDetail(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			c, rec = scopedRequest(http.MethodGet, "/admin/audit/conversation?log_id=log-1", "", tt.scope)
			require.NoError(t, h.AuditConversation(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusOK {
				return
			}
			var resp auditConversationResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			ids := make([]string, 0, len(resp.Entries))
			for _, e := range resp.Entries {
				ids = append(ids, e.ID)
			}
			if tt.scope == "" {
				assert.Equal(t, []string{"log-1", "log-2", "log-3"}, ids)
			} else {
				assert.Equal(t, []string{"log-1", "log-3"}, ids, "entries outside the scope are dropped")
			}
		})
	}
}

func TestAuthKeysScoped(t *testing.T) {
	now := time.Now().UTC()
	key := func(id, userPath string) authkeys.AuthKey {
		return authkeys.AuthKey{ID: id, Name: id, UserPath: userPath, Enabled: true, RedactedValue: "sk_gom_***", SecretHash: "hash-" + id, CreatedAt: now, UpdatedAt: now}
	}
	newHandler := func(t *testing.T) *Handler {
		t.Helper()
		return newAuthKeyHandler(t, newAuthKeyTestStore(
			key("alpha-root", scopeAlpha),
			key("alpha-child", "/team/alpha/svc"),
			key("beta", "/team/beta"),
			key("unbound", ""),
		))
	}

	t.Run("list is filtered to the scope", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodGet, "/admin/auth-keys", "", scopeAlpha)
		require.NoError(t, h.ListAuthKeys(c))
		require.Equal(t, http.StatusOK, rec.Code)
		var rows []authKeyResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		assert.ElementsMatch(t, []string{"alpha-root", "alpha-child"}, ids)

		c, rec = scopedRequest(http.MethodGet, "/admin/auth-keys", "", "")
		require.NoError(t, h.ListAuthKeys(c))
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
		assert.Len(t, rows, 4, "global scope lists every key")
	})

	t.Run("create defaults to the scope root and rejects outside paths", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodPost, "/admin/auth-keys", `{"name":"new"}`, scopeAlpha)
		require.NoError(t, h.CreateAuthKey(c))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var issued authkeys.IssuedKey
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &issued))
		assert.Equal(t, scopeAlpha, issued.UserPath)

		c, rec = scopedRequest(http.MethodPost, "/admin/auth-keys", `{"name":"new","user_path":"/team/alpha/child"}`, scopeAlpha)
		require.NoError(t, h.CreateAuthKey(c))
		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		c, rec = scopedRequest(http.MethodPost, "/admin/auth-keys", `{"name":"new","user_path":"/team/beta"}`, scopeAlpha)
		require.NoError(t, h.CreateAuthKey(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))

		c, rec = scopedRequest(http.MethodPost, "/admin/auth-keys", `{"name":"new"}`, "")
		require.NoError(t, h.CreateAuthKey(c))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var unbound authkeys.IssuedKey
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &unbound))
		assert.Empty(t, unbound.UserPath, "global scope keeps an unbound key unbound")
	})

	t.Run("updates and deactivation hide keys outside the scope", func(t *testing.T) {
		tests := []struct {
			name       string
			id         string
			wantStatus int
		}{
			{name: "own key", id: "alpha-child", wantStatus: http.StatusOK},
			{name: "other tenant key", id: "beta", wantStatus: http.StatusNotFound},
			{name: "unbound key", id: "unbound", wantStatus: http.StatusNotFound},
			{name: "unknown key", id: "missing", wantStatus: http.StatusNotFound},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := newHandler(t)
				c, rec := scopedRequest(http.MethodPut, "/admin/auth-keys/"+tt.id+"/labels", `{"labels":["x"]}`, scopeAlpha)
				c.SetPathValues(echo.PathValues{{Name: "id", Value: tt.id}})
				require.NoError(t, h.UpdateAuthKeyLabels(c))
				assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

				c, rec = scopedRequest(http.MethodPost, "/admin/auth-keys/"+tt.id+"/deactivate", "", scopeAlpha)
				c.SetPathValues(echo.PathValues{{Name: "id", Value: tt.id}})
				require.NoError(t, h.DeactivateAuthKey(c))
				want := tt.wantStatus
				if want == http.StatusOK {
					want = http.StatusNoContent
				}
				assert.Equal(t, want, rec.Code, rec.Body.String())
			})
		}
	})
}

func TestUsersScoped(t *testing.T) {
	now := time.Now().UTC()
	h := newUsersHandler(t,
		authkeys.AuthKey{ID: "k1", Name: "k1", UserPath: "/team/alpha/svc", Enabled: true, SecretHash: "h1", CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k2", Name: "k2", UserPath: "/team/beta", Enabled: true, SecretHash: "h2", CreatedAt: now, UpdatedAt: now},
	)

	c, rec := scopedRequest(http.MethodPut, "/admin/users", `{"user_path":"/team/alpha","allowed_models":["openai/"]}`, scopeAlpha)
	require.NoError(t, h.UpsertUser(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	nodes := decodeUsers(t, rec)
	assert.Contains(t, nodes, "/team/alpha")
	assert.Contains(t, nodes, "/team/alpha/svc")
	assert.NotContains(t, nodes, "/team", "ancestors above the scope are hidden")
	assert.NotContains(t, nodes, "/team/beta")
	assert.NotContains(t, nodes, "/")

	c, rec = scopedRequest(http.MethodPut, "/admin/users", `{"user_path":"/team/beta","allowed_models":["openai/"]}`, scopeAlpha)
	require.NoError(t, h.UpsertUser(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))

	c, rec = scopedRequest(http.MethodDelete, "/admin/users?user_path=/team/beta", "", scopeAlpha)
	require.NoError(t, h.DeleteUser(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))

	c, rec = scopedRequest(http.MethodDelete, "/admin/users?user_path=/team/alpha", "", scopeAlpha)
	require.NoError(t, h.DeleteUser(c))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	c, rec = scopedRequest(http.MethodGet, "/admin/users", "", "")
	require.NoError(t, h.ListUsers(c))
	nodes = decodeUsers(t, rec)
	assert.Contains(t, nodes, "/team/beta", "global scope keeps the whole tree")
	assert.Contains(t, nodes, "/")
}

func TestBudgetsScoped(t *testing.T) {
	newHandler := func(t *testing.T) *Handler {
		t.Helper()
		return newBudgetHandler(t, &adminBudgetStore{budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
			{Scope: budget.ScopeUserPath, Subject: "/team/alpha/svc", PeriodSeconds: budget.PeriodDailySeconds, Amount: 5},
			{Scope: budget.ScopeUserPath, Subject: "/team/beta", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
			{Scope: budget.ScopeLabel, Subject: "batch", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		}})
	}

	t.Run("list keeps only user_path budgets inside the scope", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodGet, "/admin/budgets", "", scopeAlpha)
		require.NoError(t, h.ListBudgets(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp budgetListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		subjects := make([]string, 0, len(resp.Budgets))
		for _, item := range resp.Budgets {
			subjects = append(subjects, item.Subject)
		}
		assert.ElementsMatch(t, []string{"/team/alpha", "/team/alpha/svc"}, subjects)

		c, rec = scopedRequest(http.MethodGet, "/admin/budgets", "", "")
		require.NoError(t, h.ListBudgets(c))
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Len(t, resp.Budgets, 4, "global scope lists every budget")
	})

	writes := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "inside scope", body: `{"user_path":"/team/alpha/new","budget_key":{"period":"daily"},"amount":1}`, wantStatus: http.StatusOK},
		{name: "outside scope", body: `{"user_path":"/team/beta","budget_key":{"period":"daily"},"amount":1}`, wantStatus: http.StatusForbidden},
		{name: "label budget", body: `{"scope":"label","subject":"batch","budget_key":{"period":"daily"},"amount":1}`, wantStatus: http.StatusForbidden},
	}
	for _, tt := range writes {
		t.Run("upsert "+tt.name, func(t *testing.T) {
			h := newHandler(t)
			c, rec := scopedRequest(http.MethodPut, "/admin/budgets", tt.body, scopeAlpha)
			require.NoError(t, h.UpsertBudget(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
			}
		})
		t.Run("delete "+tt.name, func(t *testing.T) {
			h := newHandler(t)
			c, rec := scopedRequest(http.MethodDelete, "/admin/budgets", tt.body, scopeAlpha)
			require.NoError(t, h.DeleteBudget(c))
			if tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
			} else {
				assert.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("reset-one outside scope", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodPost, "/admin/budgets/reset-one", `{"user_path":"/team/beta","period":"daily"}`, scopeAlpha)
		require.NoError(t, h.ResetBudget(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
	})
}

func TestRateLimitsScoped(t *testing.T) {
	limit := func(v int64) *int64 { return &v }
	newHandler := func(t *testing.T) *Handler {
		t.Helper()
		h, _ := newRateLimitHandler(t, &adminRateLimitStore{rules: []ratelimit.Rule{
			{Scope: ratelimit.ScopeUserPath, Subject: "/team/alpha", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: limit(10)},
			{Scope: ratelimit.ScopeUserPath, Subject: "/team/beta", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: limit(10)},
			{Scope: ratelimit.ScopeProvider, Subject: "openai", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: limit(10)},
		}})
		return h
	}

	t.Run("list keeps only user_path rules inside the scope", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodGet, "/admin/rate-limits", "", scopeAlpha)
		require.NoError(t, h.ListRateLimits(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp rateLimitListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.RateLimits, 1)
		assert.Equal(t, "/team/alpha", resp.RateLimits[0].Subject)

		c, rec = scopedRequest(http.MethodGet, "/admin/rate-limits", "", "")
		require.NoError(t, h.ListRateLimits(c))
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Len(t, resp.RateLimits, 3, "global scope lists every rule")
	})

	writes := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "inside scope", body: `{"user_path":"/team/alpha/new","limit_key":{"period":"minute"},"max_requests":1}`, wantStatus: http.StatusOK},
		{name: "outside scope", body: `{"user_path":"/team/beta","limit_key":{"period":"minute"},"max_requests":1}`, wantStatus: http.StatusForbidden},
		{name: "provider rule", body: `{"scope":"provider","subject":"openai","limit_key":{"period":"minute"},"max_requests":1}`, wantStatus: http.StatusForbidden},
	}
	for _, tt := range writes {
		t.Run("upsert "+tt.name, func(t *testing.T) {
			h := newHandler(t)
			c, rec := scopedRequest(http.MethodPut, "/admin/rate-limits", tt.body, scopeAlpha)
			require.NoError(t, h.UpsertRateLimit(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
			}
		})
		t.Run("delete "+tt.name, func(t *testing.T) {
			h := newHandler(t)
			c, rec := scopedRequest(http.MethodDelete, "/admin/rate-limits", tt.body, scopeAlpha)
			require.NoError(t, h.DeleteRateLimit(c))
			if tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
			} else {
				assert.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("reset-one outside scope", func(t *testing.T) {
		h := newHandler(t)
		c, rec := scopedRequest(http.MethodPost, "/admin/rate-limits/reset-one", `{"user_path":"/team/beta","period":"minute"}`, scopeAlpha)
		require.NoError(t, h.ResetRateLimit(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
	})
}

func TestAuditStatsScoped(t *testing.T) {
	tests := []struct {
		name       string
		scope      string
		filter     string
		wantStatus int
		wantPath   string
		wantCode   string
	}{
		{name: "global keeps empty filter", wantStatus: http.StatusOK},
		{name: "global keeps explicit filter", filter: "/team/beta", wantStatus: http.StatusOK, wantPath: "/team/beta"},
		{name: "scoped defaults to root", scope: scopeAlpha, wantStatus: http.StatusOK, wantPath: scopeAlpha},
		{name: "scoped narrows inside", scope: scopeAlpha, filter: "/team/alpha/svc", wantStatus: http.StatusOK, wantPath: "/team/alpha/svc"},
		{name: "scoped rejects outside", scope: scopeAlpha, filter: "/team/beta", wantStatus: http.StatusForbidden, wantCode: codeUserPathOutOfScope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &mockAuditReader{statsResult: auditlog.EmptyRequestStats(auditlog.StatsIntervalDay)}
			h := NewHandler(nil, nil, WithAuditReader(reader))
			target := "/admin/audit/stats?days=7"
			if tt.filter != "" {
				target += "&user_path=" + tt.filter
			}
			c, rec := scopedRequest(http.MethodGet, target, "", tt.scope)
			require.NoError(t, h.AuditStats(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantCode != "" {
				assert.Equal(t, tt.wantCode, errorCode(t, rec))
				return
			}
			assert.Equal(t, tt.wantPath, reader.lastStatsParams.UserPath)
		})
	}
}
