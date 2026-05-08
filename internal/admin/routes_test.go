package admin

import (
	"sort"
	"testing"

	"github.com/labstack/echo/v5"
)

// TestRegisterRoutes_RegistersExpectedPaths is a smoke test for the admin
// RouteRegistrar plumbing. It mounts the handler on a real echo router and
// verifies that every method+path the route table claims to register is
// actually known to the router after RegisterRoutes returns.
//
// The intent is to catch regressions when handlers are added or renamed
// without updating routes.go (or vice-versa) — including typos and missing
// wires that would otherwise only surface in production traffic.
func TestRegisterRoutes_RegistersExpectedPaths(t *testing.T) {
	h := &Handler{}
	e := echo.New()
	g := e.Group("/admin/api/v1")

	// RegisterRoutes must not panic with a zero-value handler — every endpoint
	// reads its own dependencies inside the handler body, so route mounting
	// itself must remain side-effect-free.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoutes panicked: %v", r)
		}
	}()
	h.RegisterRoutes(g)

	want := []string{
		"GET /admin/api/v1/dashboard/config",
		"GET /admin/api/v1/cache/overview",

		"GET /admin/api/v1/usage/summary",
		"GET /admin/api/v1/usage/daily",
		"GET /admin/api/v1/usage/models",
		"GET /admin/api/v1/usage/user-paths",
		"GET /admin/api/v1/usage/log",
		"POST /admin/api/v1/usage/recalculate-pricing",

		"GET /admin/api/v1/audit/log",
		"GET /admin/api/v1/audit/conversation",

		"GET /admin/api/v1/providers/status",
		"POST /admin/api/v1/runtime/refresh",

		"GET /admin/api/v1/budgets",
		"PUT /admin/api/v1/budgets",
		"DELETE /admin/api/v1/budgets",
		"GET /admin/api/v1/budgets/settings",
		"PUT /admin/api/v1/budgets/settings",
		"POST /admin/api/v1/budgets/reset-one",
		"POST /admin/api/v1/budgets/reset",

		"GET /admin/api/v1/models",
		"GET /admin/api/v1/models/categories",

		"GET /admin/api/v1/model-overrides",
		"PUT /admin/api/v1/model-overrides",
		"DELETE /admin/api/v1/model-overrides",

		"GET /admin/api/v1/model-pricing-overrides",
		"PUT /admin/api/v1/model-pricing-overrides",
		"DELETE /admin/api/v1/model-pricing-overrides",

		"GET /admin/api/v1/auth-keys",
		"POST /admin/api/v1/auth-keys",
		"POST /admin/api/v1/auth-keys/:id/deactivate",

		"GET /admin/api/v1/aliases",
		"PUT /admin/api/v1/aliases",
		"DELETE /admin/api/v1/aliases",

		"GET /admin/api/v1/guardrails/types",
		"GET /admin/api/v1/guardrails",
		"PUT /admin/api/v1/guardrails",
		"DELETE /admin/api/v1/guardrails",

		"GET /admin/api/v1/workflows",
		"GET /admin/api/v1/workflows/guardrails",
		"GET /admin/api/v1/workflows/:id",
		"POST /admin/api/v1/workflows",
		"POST /admin/api/v1/workflows/:id/deactivate",
	}

	registered := make(map[string]struct{})
	for _, route := range e.Router().Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	sort.Strings(want)
	missing := make([]string, 0)
	for _, key := range want {
		if _, ok := registered[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("RegisterRoutes did not register %d route(s):\n  %s", len(missing), missing)
	}

	if got, expected := len(registered), len(want); got != expected {
		extras := make([]string, 0)
		wantSet := make(map[string]struct{}, len(want))
		for _, k := range want {
			wantSet[k] = struct{}{}
		}
		for k := range registered {
			if _, ok := wantSet[k]; !ok {
				extras = append(extras, k)
			}
		}
		sort.Strings(extras)
		t.Fatalf("RegisterRoutes registered %d route(s), want %d; extras: %v", got, expected, extras)
	}
}
