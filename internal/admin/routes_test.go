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
	g := e.Group("/admin")

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
		"GET /admin/runtime/config",
		"GET /admin/cache/overview",
		"GET /admin/live/logs",

		"GET /admin/usage/summary",
		"GET /admin/usage/daily",
		"GET /admin/usage/models",
		"GET /admin/usage/user-paths",
		"GET /admin/usage/log",
		"POST /admin/usage/recalculate-pricing",

		"GET /admin/audit/log",
		"GET /admin/audit/detail",
		"GET /admin/audit/conversation",

		"GET /admin/providers/status",
		"POST /admin/runtime/refresh",

		"GET /admin/budgets",
		"PUT /admin/budgets",
		"DELETE /admin/budgets",
		"GET /admin/budgets/settings",
		"PUT /admin/budgets/settings",
		"POST /admin/budgets/reset-one",
		"POST /admin/budgets/reset",

		"GET /admin/models",
		"GET /admin/models/categories",

		"GET /admin/virtual-models",
		"PUT /admin/virtual-models",
		"DELETE /admin/virtual-models",

		"GET /admin/model-pricing-overrides",
		"PUT /admin/model-pricing-overrides",
		"DELETE /admin/model-pricing-overrides",

		"GET /admin/auth-keys",
		"POST /admin/auth-keys",
		"POST /admin/auth-keys/:id/deactivate",

		"GET /admin/guardrails/types",
		"GET /admin/guardrails",
		"PUT /admin/guardrails",
		"DELETE /admin/guardrails",

		"GET /admin/workflows",
		"GET /admin/workflows/guardrails",
		"GET /admin/workflows/:id",
		"POST /admin/workflows",
		"POST /admin/workflows/:id/deactivate",
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
