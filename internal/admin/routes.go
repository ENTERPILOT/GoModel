package admin

import "github.com/labstack/echo/v5"

// RouteRegistrar is the subset of *echo.Group / *echo.Echo that RegisterRoutes
// uses. Decoupling from a concrete echo type keeps the admin package useful for
// callers that want to mount the API under a different path prefix or wrap the
// routes with extra middleware.
type RouteRegistrar interface {
	GET(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo
	POST(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo
	PUT(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo
	DELETE(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo
}

// RegisterRoutes mounts the admin REST API on the given route group.
// Callers typically pass an *echo.Group rooted at /admin.
//
// Routes carrying the global middleware are gateway-wide: user-path scoped
// credentials get 403 admin_scope_denied there. Every other route either
// serves tenant data filtered to the caller's scope or is harmless metadata.
func (h *Handler) RegisterRoutes(g RouteRegistrar) {
	global := RequireGlobalScope()

	g.GET("/access", h.Access)
	g.GET("/runtime/config", h.DashboardConfig)
	g.GET("/runtime/settings", h.RuntimeSettings, global)
	g.PUT("/runtime/settings/:key", h.UpdateRuntimeSetting, global)
	g.GET("/cache/overview", h.CacheOverview, global)
	g.GET("/live/logs", h.LiveLogs, global)

	g.GET("/usage/summary", h.UsageSummary)
	g.GET("/usage/daily", h.DailyUsage)
	g.GET("/usage/models", h.UsageByModel)
	g.GET("/usage/user-paths", h.UsageByUserPath)
	g.GET("/usage/labels", h.UsageByLabel)
	g.GET("/usage/sessions", h.UsageBySession)
	g.GET("/usage/log", h.UsageLog)
	g.GET("/usage/throughput", h.TokenThroughput, global)
	g.POST("/usage/recalculate-pricing", h.RecalculateUsagePricing, global)

	g.GET("/audit/log", h.AuditLog)
	g.GET("/audit/sessions", h.AuditSessions)
	g.GET("/audit/stats", h.AuditStats)
	g.GET("/audit/detail", h.AuditLogDetail)
	g.GET("/audit/conversation", h.AuditConversation)

	g.GET("/providers/status", h.ProviderStatus, global)
	g.POST("/runtime/refresh", h.RefreshRuntime, global)

	g.GET("/provider-credentials", h.ListProviderCredentials, global)
	g.GET("/provider-credentials/types", h.ProviderCredentialTypes, global)
	g.PUT("/provider-credentials", h.UpsertProviderCredential, global)
	g.DELETE("/provider-credentials/:name", h.DeleteProviderCredential, global)

	g.GET("/budgets", h.ListBudgets)
	g.PUT("/budgets", h.UpsertBudget)
	g.DELETE("/budgets", h.DeleteBudget)
	g.GET("/budgets/settings", h.BudgetSettings)
	g.PUT("/budgets/settings", h.UpdateBudgetSettings, global)
	g.POST("/budgets/reset-one", h.ResetBudget)
	g.POST("/budgets/reset", h.ResetBudgets, global)

	g.GET("/rate-limits", h.ListRateLimits)
	g.PUT("/rate-limits", h.UpsertRateLimit)
	g.DELETE("/rate-limits", h.DeleteRateLimit)
	g.POST("/rate-limits/reset-one", h.ResetRateLimit)
	g.POST("/rate-limits/reset", h.ResetRateLimits, global)

	g.GET("/tagging/settings", h.TaggingSettings, global)
	g.PUT("/tagging/settings", h.UpdateTaggingSettings, global)

	g.GET("/models", h.ListModels)
	g.GET("/models/categories", h.ListCategories)

	g.GET("/virtual-models", h.ListVirtualModels, global)
	g.PUT("/virtual-models", h.UpsertVirtualModel, global)
	g.DELETE("/virtual-models", h.DeleteVirtualModel, global)

	g.GET("/mcp-servers", h.ListMCPServers, global)
	g.PUT("/mcp-servers", h.UpsertMCPServer, global)
	g.DELETE("/mcp-servers/:name", h.DeleteMCPServer, global)
	g.POST("/mcp-servers/:name/reconnect", h.ReconnectMCPServer, global)
	g.GET("/mcp-servers/:name/catalog", h.MCPServerCatalog, global)

	g.GET("/model-pricing-overrides", h.ListModelPricingOverrides, global)
	g.PUT("/model-pricing-overrides", h.UpsertModelPricingOverride, global)
	g.DELETE("/model-pricing-overrides", h.DeleteModelPricingOverride, global)

	g.GET("/auth-keys", h.ListAuthKeys)
	g.POST("/auth-keys", h.CreateAuthKey)
	g.PUT("/auth-keys/:id/labels", h.UpdateAuthKeyLabels)
	g.PUT("/auth-keys/:id/allowed-models", h.UpdateAuthKeyAllowedModels)
	g.PUT("/auth-keys/:id/dashboard-access", h.UpdateAuthKeyDashboardAccess)
	g.POST("/auth-keys/:id/deactivate", h.DeactivateAuthKey)

	g.GET("/users", h.ListUsers)
	g.PUT("/users", h.UpsertUser)
	g.DELETE("/users", h.DeleteUser)

	g.GET("/plugins", h.ListPlugins, global)
	g.GET("/guardrails/types", h.ListGuardrailTypes, global)
	g.GET("/guardrails", h.ListGuardrails, global)
	g.PUT("/guardrails", h.UpsertGuardrail, global)
	g.DELETE("/guardrails", h.DeleteGuardrail, global)

	g.GET("/workflows", h.ListWorkflows, global)
	g.GET("/workflows/guardrails", h.ListWorkflowGuardrails, global)
	g.GET("/workflows/:id", h.GetWorkflow, global)
	g.POST("/workflows", h.CreateWorkflow, global)
	g.POST("/workflows/:id/deactivate", h.DeactivateWorkflow, global)
}
