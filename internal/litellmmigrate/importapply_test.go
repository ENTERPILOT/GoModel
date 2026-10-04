package litellmmigrate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adminCall struct {
	Method string
	Path   string
	Auth   string
	Body   map[string]any
}

type fakeAdmin struct {
	mu     sync.Mutex
	calls  []adminCall
	access func(w http.ResponseWriter)
	reply  func(call adminCall, w http.ResponseWriter)
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/access" {
		if f.access != nil {
			f.access(w)
			return
		}
		_, _ = io.WriteString(w, `{"scope":"global"}`)
		return
	}
	call := adminCall{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
	_ = json.NewDecoder(r.Body).Decode(&call.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.reply != nil {
		f.reply(call, w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func applyTestPlan() *ImportPlan {
	return &ImportPlan{
		Policies:   []PolicyImport{{UserPath: "/team", AllowedModels: []string{"openai/gpt-4o"}, Description: "Migrated from LiteLLM team team"}},
		Budgets:    []BudgetImport{{UserPath: "/team", Period: "monthly", Amount: 100}, {UserPath: "/team/k", PeriodSeconds: 43200, Amount: 2}},
		RateLimits: []RateLimitImport{{UserPath: "/team/k", Period: "concurrent", MaxRequests: new(int64(4))}},
		Keys: []KeyImport{
			{Name: "new", ImportedFrom: "litellm", SecretHash: "h-new", UserPath: "/team/k"},
			{Name: "old", ImportedFrom: "litellm", SecretHash: "h-old", UserPath: "/team/old"},
		},
	}
}

func TestApply_WritesPlanInOrder(t *testing.T) {
	admin := &fakeAdmin{reply: func(call adminCall, w http.ResponseWriter) {
		if call.Path == "/admin/auth-keys/import" && call.Body["secret_hash"] == "h-old" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}}
	server := httptest.NewServer(admin)
	defer server.Close()

	result, err := Apply(context.Background(), server.Client(), server.URL+"/", "sk-admin", applyTestPlan())
	require.NoError(t, err)
	assert.Equal(t, ApplyResult{Policies: 1, Budgets: 2, RateLimits: 1, Keys: 1, KeysExisting: 1}, result)

	require.Len(t, admin.calls, 6)
	order := make([]string, len(admin.calls))
	for i, call := range admin.calls {
		order[i] = call.Method + " " + call.Path
		assert.Equal(t, "Bearer sk-admin", call.Auth)
	}
	assert.Equal(t, []string{
		"PUT /admin/users", "PUT /admin/budgets", "PUT /admin/budgets", "PUT /admin/rate-limits",
		"POST /admin/auth-keys/import", "POST /admin/auth-keys/import",
	}, order, "policies go first, keys last")
	assert.Equal(t, map[string]any{"user_path": "/team", "allowed_models": []any{"openai/gpt-4o"}, "description": "Migrated from LiteLLM team team"}, admin.calls[0].Body)
	assert.Equal(t, map[string]any{"scope": "user_path", "subject": "/team", "amount": 100.0, "budget_key": map[string]any{"period": "monthly"}}, admin.calls[1].Body)
	assert.Equal(t, map[string]any{"period_seconds": 43200.0}, admin.calls[2].Body["budget_key"])
	assert.Equal(t, map[string]any{"scope": "user_path", "subject": "/team/k", "limit_key": map[string]any{"period": "concurrent"}, "max_requests": 4.0, "max_tokens": nil}, admin.calls[3].Body)
	assert.Equal(t, map[string]any{"name": "new", "imported_from": "litellm", "secret_hash": "h-new", "user_path": "/team/k"}, admin.calls[4].Body)
}

func TestApply_RecordsRejectedWritesAndContinues(t *testing.T) {
	admin := &fakeAdmin{reply: func(call adminCall, w http.ResponseWriter) {
		if call.Path == "/admin/budgets" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"budgets feature is unavailable"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}}
	server := httptest.NewServer(admin)
	defer server.Close()

	result, err := Apply(context.Background(), server.Client(), server.URL, "sk-admin", applyTestPlan())
	require.NoError(t, err)
	assert.Equal(t, 0, result.Budgets)
	assert.Equal(t, 2, result.Keys)
	assert.Equal(t, []string{
		"budget /team: PUT /budgets: 503 budgets feature is unavailable",
		"budget /team/k: PUT /budgets: 503 budgets feature is unavailable",
	}, result.Failures)
}

func TestApply_ChecksTheAdminKeyBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		access func(w http.ResponseWriter)
		want   string
	}{
		{"rejected key", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid master key"}}`)
		}, "GoModel admin API: 401 invalid master key"},
		{"scoped key", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"scope":"user_path","user_path":"/team"}`)
		}, "the admin key is scoped to a user path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin := &fakeAdmin{access: tt.access}
			server := httptest.NewServer(admin)
			defer server.Close()

			_, err := Apply(context.Background(), server.Client(), server.URL, "sk-wrong", applyTestPlan())
			require.ErrorContains(t, err, tt.want)
			assert.Empty(t, admin.calls, "nothing is written")
		})
	}
}
