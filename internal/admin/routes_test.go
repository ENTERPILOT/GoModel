package admin

import (
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
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
		r := recover()
		require.Nil(t, r)
	}()
	h.RegisterRoutes(g)

	want := []string{
		"GET /admin/access",
		"GET /admin/runtime/config",
		"GET /admin/cache/overview",
		"GET /admin/live/logs",
		"GET /admin/runtime/settings",
		"PUT /admin/runtime/settings/:key",

		"GET /admin/usage/summary",
		"GET /admin/usage/daily",
		"GET /admin/usage/models",
		"GET /admin/usage/user-paths",
		"GET /admin/usage/labels",
		"GET /admin/usage/sessions",
		"GET /admin/usage/log",
		"GET /admin/usage/throughput",
		"POST /admin/usage/recalculate-pricing",

		"GET /admin/audit/log",
		"GET /admin/audit/sessions",
		"GET /admin/audit/stats",
		"GET /admin/audit/detail",
		"GET /admin/audit/conversation",

		"GET /admin/providers/status",
		"POST /admin/runtime/refresh",

		"GET /admin/provider-credentials",
		"GET /admin/provider-credentials/types",
		"PUT /admin/provider-credentials",
		"DELETE /admin/provider-credentials/:name",

		"GET /admin/budgets",
		"PUT /admin/budgets",
		"DELETE /admin/budgets",
		"GET /admin/budgets/settings",
		"PUT /admin/budgets/settings",
		"POST /admin/budgets/reset-one",
		"POST /admin/budgets/reset",

		"GET /admin/rate-limits",
		"PUT /admin/rate-limits",
		"DELETE /admin/rate-limits",
		"POST /admin/rate-limits/reset-one",
		"POST /admin/rate-limits/reset",

		"GET /admin/tagging/settings",
		"PUT /admin/tagging/settings",

		"GET /admin/models",
		"GET /admin/models/categories",

		"GET /admin/virtual-models",
		"PUT /admin/virtual-models",
		"DELETE /admin/virtual-models",

		"GET /admin/mcp-servers",
		"PUT /admin/mcp-servers",
		"DELETE /admin/mcp-servers/:name",
		"POST /admin/mcp-servers/:name/reconnect",
		"GET /admin/mcp-servers/:name/catalog",

		"GET /admin/model-pricing-overrides",
		"PUT /admin/model-pricing-overrides",
		"DELETE /admin/model-pricing-overrides",

		"GET /admin/auth-keys",
		"POST /admin/auth-keys",
		"PUT /admin/auth-keys/:id/labels",
		"PUT /admin/auth-keys/:id/allowed-models",
		"PUT /admin/auth-keys/:id/dashboard-access",
		"POST /admin/auth-keys/:id/deactivate",

		"GET /admin/users",
		"PUT /admin/users",
		"DELETE /admin/users",

		"GET /admin/plugins",
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

	registered := make([]string, 0, len(want))
	for _, route := range e.Router().Routes() {
		registered = append(registered, route.Method+" "+route.Path)
	}
	require.ElementsMatch(t, want, registered)
}
