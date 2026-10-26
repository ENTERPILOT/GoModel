// Package admin provides the admin REST API and dashboard for GoModel.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/mediastore"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// Handler serves admin API endpoints.
type Handler struct {
	usageReader         usage.UsageReader
	usageRecalculator   usage.PricingRecalculator
	auditReader         auditlog.Reader
	registry            *providers.ModelRegistry
	pricingResolver     usage.PricingResolver
	billing             usage.Billing
	authKeys            *authkeys.Service
	users               *users.Service
	virtualModels       *virtualmodels.Service
	mcpServers          MCPServerAdmin
	pricingOverrides    *pricingoverrides.Service
	workflows           *workflows.Service
	budgets             *budget.Service
	rateLimits          *ratelimit.Service
	tagging             *tagging.Service
	runtimeSettings     *runtimesettings.Service
	guardrails          guardrails.Catalog
	guardrailDefs       *guardrails.Service
	pluginCatalog       *plugins.Catalog
	liveBroker          *live.Broker
	runtimeConfig       DashboardConfigResponse
	runtimeRefresher    RuntimeRefresher
	configuredProviders []providers.SanitizedProviderConfig
	providerCredentials ProviderCredentialsAdmin
	requestHealth       RequestHealthSource
	media               *mediastore.Service
	quotaTemplates      bool
	billedBudgets       bool

	mutationMu sync.Mutex
	pricingMu  sync.Mutex
}

// Option configures the admin API handler.
type Option func(*Handler)

const (
	DashboardConfigDemoMode             = "DEMO_MODE"
	DashboardConfigFailoverEnabled      = "FAILOVER_ENABLED"
	DashboardConfigLoggingEnabled       = "LOGGING_ENABLED"
	DashboardConfigLoggingRetentionDays = "LOGGING_RETENTION_DAYS"
	DashboardConfigUsageEnabled         = "USAGE_ENABLED"
	DashboardConfigBudgetsEnabled       = "BUDGETS_ENABLED"
	DashboardConfigRateLimitsEnabled    = "RATE_LIMITS_ENABLED"
	DashboardConfigQuotaTemplates       = "PER_CHILD_QUOTAS_ENABLED"
	DashboardConfigBilledBudgets        = "BILLED_BUDGETS_ENABLED"
	DashboardConfigGuardrailsEnabled    = "GUARDRAILS_ENABLED"
	DashboardConfigPluginsEnabled       = "PLUGINS_ENABLED"
	DashboardConfigCacheEnabled         = "CACHE_ENABLED"
	DashboardConfigRedisURL             = "REDIS_URL"
	DashboardConfigSemanticCacheEnabled = "SEMANTIC_CACHE_ENABLED"
	DashboardConfigPricingRecalculation = "USAGE_PRICING_RECALCULATION_ENABLED"
	DashboardConfigPricingMarkups       = "PRICING_MARKUPS_ENABLED"
	DashboardConfigLiveLogsEnabled      = "DASHBOARD_LIVE_LOGS_ENABLED"
	DashboardConfigMCPEnabled           = "MCP_ENABLED"
	DashboardConfigMCPToolDiscovery     = "MCP_TOOL_DISCOVERY"
	DashboardConfigVMStrategies         = "VIRTUAL_MODEL_STRATEGIES"
	DashboardConfigUserPathHeader       = "USER_PATH_HEADER"
	DashboardConfigLicenseBadge         = "LICENSE_BADGE"
	DashboardConfigLicenseTrial         = "LICENSE_TRIAL"
	DashboardConfigLicenseExpiresAt     = "LICENSE_EXPIRES_AT"
	DashboardConfigLicenseValidUntil    = "LICENSE_VALID_UNTIL"
	DashboardConfigLicenseNoticeDays    = "LICENSE_NOTICE_DAYS"
	DashboardConfigLicenseWarningDays   = "LICENSE_WARNING_DAYS"
)

// statusClientClosedRequest is the de facto status used by proxies for client-aborted requests.
const statusClientClosedRequest = 499

// DashboardConfigResponse is the allowlisted runtime config contract exposed to the dashboard UI.
type DashboardConfigResponse struct {
	DemoMode              string `json:"DEMO_MODE,omitempty"`
	FailoverEnabled       string `json:"FAILOVER_ENABLED,omitempty"`
	LoggingEnabled        string `json:"LOGGING_ENABLED,omitempty"`
	LoggingRetentionDays  string `json:"LOGGING_RETENTION_DAYS,omitempty"`
	UsageEnabled          string `json:"USAGE_ENABLED,omitempty"`
	BudgetsEnabled        string `json:"BUDGETS_ENABLED,omitempty"`
	RateLimitsEnabled     string `json:"RATE_LIMITS_ENABLED,omitempty"`
	QuotaTemplatesEnabled string `json:"PER_CHILD_QUOTAS_ENABLED,omitempty"`
	// BilledBudgetsEnabled reports the billed_budgets capability, so the
	// dashboard offers budgets on billed cost only where they are accepted.
	BilledBudgetsEnabled string `json:"BILLED_BUDGETS_ENABLED,omitempty"`
	GuardrailsEnabled    string `json:"GUARDRAILS_ENABLED,omitempty"`
	PluginsEnabled       string `json:"PLUGINS_ENABLED,omitempty"`
	CacheEnabled         string `json:"CACHE_ENABLED,omitempty"`
	RedisURL             string `json:"REDIS_URL,omitempty"`
	SemanticCacheEnabled string `json:"SEMANTIC_CACHE_ENABLED,omitempty"`
	PricingRecalculation string `json:"USAGE_PRICING_RECALCULATION_ENABLED,omitempty"`
	// PricingMarkups mirrors pricing.markups_enabled, so the dashboard shows
	// billed cost only when the markup feature is on.
	PricingMarkups  string `json:"PRICING_MARKUPS_ENABLED,omitempty"`
	LiveLogsEnabled string `json:"DASHBOARD_LIVE_LOGS_ENABLED,omitempty"`
	MCPEnabled      string `json:"MCP_ENABLED,omitempty"`
	// MCPToolDiscovery is the gateway's default MCP tool discovery mode
	// ("off" or "search"), so the dashboard can show how clients see tools.
	MCPToolDiscovery string `json:"MCP_TOOL_DISCOVERY,omitempty"`
	// VirtualModelStrategies is the comma-separated list of load-balancing
	// strategies this deployment supports. "adaptive" appears only when a
	// route-selector extension is registered, so the dashboard never offers
	// a strategy that would silently fall back to round robin.
	VirtualModelStrategies string `json:"VIRTUAL_MODEL_STRATEGIES,omitempty"`
	// UserPathHeader is the canonical name of the inbound header the gateway
	// reads user paths from (server.user_path_header), so the Playground sends
	// the header this deployment actually honors.
	UserPathHeader string `json:"USER_PATH_HEADER,omitempty"`
	// The license a distribution registered (ext.License), so the sidebar
	// shows its badge and the days left. Times are RFC 3339 in UTC, windows
	// whole days; all are empty for open-source builds.
	LicenseBadge       string `json:"LICENSE_BADGE,omitempty"`
	LicenseTrial       string `json:"LICENSE_TRIAL,omitempty"`
	LicenseExpiresAt   string `json:"LICENSE_EXPIRES_AT,omitempty"`
	LicenseValidUntil  string `json:"LICENSE_VALID_UNTIL,omitempty"`
	LicenseNoticeDays  string `json:"LICENSE_NOTICE_DAYS,omitempty"`
	LicenseWarningDays string `json:"LICENSE_WARNING_DAYS,omitempty"`
}

type providerStatusSummaryResponse struct {
	Total         int    `json:"total"`
	Healthy       int    `json:"healthy"`
	Degraded      int    `json:"degraded"`
	Unhealthy     int    `json:"unhealthy"`
	OverallStatus string `json:"overall_status"`
}

type providerStatusItemResponse struct {
	Name         string                            `json:"name"`
	Type         string                            `json:"type"`
	Status       string                            `json:"status"`
	StatusLabel  string                            `json:"status_label"`
	StatusReason string                            `json:"status_reason"`
	LastError    string                            `json:"last_error,omitempty"`
	Config       providers.SanitizedProviderConfig `json:"config"`
	Runtime      providers.ProviderRuntimeSnapshot `json:"runtime"`
	// RequestHealth reports windowed real-traffic outcomes (per-model error
	// counts and the live circuit-breaker state); nil when the provider has
	// served no recent requests or request-health tracking is not wired.
	RequestHealth *health.ProviderHealth `json:"request_health,omitempty"`
}

type providerStatusResponse struct {
	Summary   providerStatusSummaryResponse `json:"summary"`
	Providers []providerStatusItemResponse  `json:"providers"`
}

type auditLogEntryResponse struct {
	auditlog.LogEntry
	Usage *usage.RequestUsageSummary `json:"usage,omitempty"`

	// BodiesOmitted marks a list entry whose request/response bodies (and
	// other heavy payloads) were stripped server-side; the full entry is
	// available from GET /admin/audit/detail. It also tells the dashboard
	// the entry is persisted — a slim entry is never an in-flight request.
	BodiesOmitted bool `json:"bodies_omitted,omitempty"`

	// ConversationPayload reports that the entry's (stripped) bodies were
	// shaped like a conversation, preserving the Interactions-drawer
	// eligibility signal the dashboard otherwise sniffs from the bodies.
	ConversationPayload bool `json:"conversation_payload,omitempty"`
}

type auditLogListResponse struct {
	Entries []auditLogEntryResponse `json:"entries"`
	Total   int                     `json:"total"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
}

type auditConversationResponse struct {
	AnchorID string                  `json:"anchor_id"`
	Entries  []auditLogEntryResponse `json:"entries"`

	// Truncated reports that more session or linkage entries exist than were
	// returned.
	Truncated bool `json:"truncated,omitempty"`
}

type auditSessionResponse struct {
	RequestCount int                   `json:"request_count"`
	Latest       auditLogEntryResponse `json:"latest"`
}

type auditSessionsListResponse struct {
	Sessions []auditSessionResponse `json:"sessions"`
	Total    int                    `json:"total"`
	Limit    int                    `json:"limit"`
	Offset   int                    `json:"offset"`
}

const (
	RuntimeRefreshStatusOK      = "ok"
	RuntimeRefreshStatusPartial = "partial"
	RuntimeRefreshStatusFailed  = "failed"
	RuntimeRefreshStatusSkipped = "skipped"
)

// RuntimeRefreshStep describes the result of one manual runtime refresh step.
type RuntimeRefreshStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// RuntimeRefreshReport is returned by the manual runtime refresh endpoint.
type RuntimeRefreshReport struct {
	Status        string               `json:"status"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at"`
	DurationMS    int64                `json:"duration_ms"`
	ModelCount    int                  `json:"model_count"`
	ProviderCount int                  `json:"provider_count"`
	Steps         []RuntimeRefreshStep `json:"steps"`
}

// RuntimeRefresher refreshes application runtime snapshots on demand.
type RuntimeRefresher interface {
	RefreshRuntime(ctx context.Context) (RuntimeRefreshReport, error)
}

// WithAuditReader enables audit log read endpoints.
func WithAuditReader(reader auditlog.Reader) Option {
	return func(h *Handler) {
		h.auditReader = reader
	}
}

// WithUsagePricingRecalculator enables persisted usage pricing recalculation.
func WithUsagePricingRecalculator(recalculator usage.PricingRecalculator) Option {
	return func(h *Handler) {
		h.usageRecalculator = recalculator
	}
}

// WithPricingResolver sets the resolver used for usage pricing recalculation.
func WithPricingResolver(resolver usage.PricingResolver) Option {
	return func(h *Handler) {
		h.pricingResolver = resolver
	}
}

// WithBilling sets how pricing recalculation treats billed cost, mirroring
// pricing.markups_enabled and pricing.bill_cache_hits. Default: billing off,
// so recalculation leaves billed cost unchanged.
func WithBilling(billing usage.Billing) Option {
	return func(h *Handler) {
		h.billing = billing
	}
}

// WithVirtualModels enables unified virtual model administration endpoints.
func WithVirtualModels(service *virtualmodels.Service) Option {
	return func(h *Handler) {
		h.virtualModels = service
	}
}

// WithMCPServers enables MCP server administration endpoints. Callers must
// not wrap a nil *mcpgateway.Service (a typed nil would defeat the handlers'
// feature-unavailable check).
func WithMCPServers(service MCPServerAdmin) Option {
	return func(h *Handler) {
		h.mcpServers = service
	}
}

// WithProviderCredentials enables admin management of model provider
// credentials (the dashboard alternative to setting provider API keys as
// env vars). Callers must not wrap a nil *providers.CredentialsService (a
// typed nil would defeat the handlers' feature-unavailable check).
func WithProviderCredentials(service ProviderCredentialsAdmin) Option {
	return func(h *Handler) {
		h.providerCredentials = service
	}
}

// WithAuthKeys enables managed auth key administration endpoints.
func WithAuthKeys(service *authkeys.Service) Option {
	return func(h *Handler) {
		h.authKeys = service
	}
}

// WithUsers enables user (user-path) access policy administration endpoints.
func WithUsers(service *users.Service) Option {
	return func(h *Handler) {
		h.users = service
	}
}

// WithPricingOverrides enables model pricing override administration endpoints.
func WithPricingOverrides(service *pricingoverrides.Service) Option {
	return func(h *Handler) {
		h.pricingOverrides = service
	}
}

// WithWorkflows enables workflow administration endpoints.
func WithWorkflows(service *workflows.Service) Option {
	return func(h *Handler) {
		h.workflows = service
	}
}

// WithBudgets enables budget administration endpoints.
func WithBudgets(service *budget.Service) Option {
	return func(h *Handler) {
		h.budgets = service
	}
}

// WithRateLimits enables rate limit administration endpoints.
func WithRateLimits(service *ratelimit.Service) Option {
	return func(h *Handler) {
		h.rateLimits = service
	}
}

// WithQuotaTemplatesEnabled controls whether admin writes may create
// per-child budget and rate-limit templates.
func WithQuotaTemplatesEnabled(enabled bool) Option {
	return func(h *Handler) {
		h.quotaTemplates = enabled
	}
}

// WithBilledBudgetsEnabled controls whether admin writes may create budgets on
// billed cost. They also need billing (WithBilling) enabled.
func WithBilledBudgetsEnabled(enabled bool) Option {
	return func(h *Handler) {
		h.billedBudgets = enabled
	}
}

// WithTagging wires the header tagging service for label rule management.
func WithTagging(service *tagging.Service) Option {
	return func(h *Handler) {
		h.tagging = service
	}
}

// WithPluginCatalog enables the plugin listing endpoint.
func WithPluginCatalog(catalog *plugins.Catalog) Option {
	return func(h *Handler) {
		h.pluginCatalog = catalog
	}
}

// WithGuardrailService enables full guardrail definition administration
// endpoints. A nil service (plugin system disabled) leaves them unavailable
// rather than storing a nil pointer behind the interfaces.
func WithGuardrailService(service *guardrails.Service) Option {
	return func(h *Handler) {
		if service == nil {
			return
		}
		h.guardrails = service
		h.guardrailDefs = service
	}
}

// WithLiveBroker enables realtime dashboard log previews.
func WithLiveBroker(broker *live.Broker) Option {
	return func(h *Handler) {
		h.liveBroker = broker
	}
}

// RequestHealthSource supplies windowed real-traffic health per provider,
// keyed by configured provider name.
type RequestHealthSource interface {
	Snapshot() map[string]health.ProviderHealth
}

// WithRequestHealth folds recent request outcomes (per-model errors and the
// live circuit-breaker state) into the provider status endpoint.
func WithRequestHealth(source RequestHealthSource) Option {
	return func(h *Handler) {
		h.requestHealth = source
	}
}

// WithDashboardRuntimeConfig enables the allowlisted dashboard runtime config endpoint.
func WithDashboardRuntimeConfig(values DashboardConfigResponse) Option {
	return func(h *Handler) {
		h.runtimeConfig = normalizeDashboardRuntimeConfig(values)
	}
}

// WithRuntimeRefresher enables manual runtime refresh from the admin API.
func WithRuntimeRefresher(refresher RuntimeRefresher) Option {
	return func(h *Handler) {
		h.runtimeRefresher = refresher
	}
}

// WithRuntimeSettings enables deployment-wide extension settings.
func WithRuntimeSettings(settings *runtimesettings.Service) Option {
	return func(h *Handler) {
		h.runtimeSettings = settings
	}
}

// WithConfiguredProviders enables the admin-safe provider inventory endpoint.
func WithConfiguredProviders(configs []providers.SanitizedProviderConfig) Option {
	return func(h *Handler) {
		h.configuredProviders = cloneConfiguredProviders(configs)
	}
}

// NewHandler creates a new admin API handler.
// usageReader may be nil if usage tracking is not available.
func NewHandler(reader usage.UsageReader, registry *providers.ModelRegistry, options ...Option) *Handler {
	h := &Handler{
		usageReader:   reader,
		registry:      registry,
		runtimeConfig: DashboardConfigResponse{},
	}
	if registry != nil {
		h.pricingResolver = registry
	}

	for _, opt := range options {
		if opt != nil {
			opt(h)
		}
	}

	return h
}

func normalizeDashboardRuntimeConfig(values DashboardConfigResponse) DashboardConfigResponse {
	return DashboardConfigResponse{
		DemoMode:               strings.TrimSpace(values.DemoMode),
		FailoverEnabled:        strings.TrimSpace(values.FailoverEnabled),
		LoggingEnabled:         strings.TrimSpace(values.LoggingEnabled),
		LoggingRetentionDays:   strings.TrimSpace(values.LoggingRetentionDays),
		UsageEnabled:           strings.TrimSpace(values.UsageEnabled),
		BudgetsEnabled:         strings.TrimSpace(values.BudgetsEnabled),
		RateLimitsEnabled:      strings.TrimSpace(values.RateLimitsEnabled),
		QuotaTemplatesEnabled:  strings.TrimSpace(values.QuotaTemplatesEnabled),
		BilledBudgetsEnabled:   strings.TrimSpace(values.BilledBudgetsEnabled),
		GuardrailsEnabled:      strings.TrimSpace(values.GuardrailsEnabled),
		PluginsEnabled:         strings.TrimSpace(values.PluginsEnabled),
		CacheEnabled:           strings.TrimSpace(values.CacheEnabled),
		RedisURL:               strings.TrimSpace(values.RedisURL),
		SemanticCacheEnabled:   strings.TrimSpace(values.SemanticCacheEnabled),
		PricingRecalculation:   strings.TrimSpace(values.PricingRecalculation),
		PricingMarkups:         strings.TrimSpace(values.PricingMarkups),
		LiveLogsEnabled:        strings.TrimSpace(values.LiveLogsEnabled),
		MCPEnabled:             strings.TrimSpace(values.MCPEnabled),
		MCPToolDiscovery:       strings.TrimSpace(values.MCPToolDiscovery),
		VirtualModelStrategies: strings.TrimSpace(values.VirtualModelStrategies),
		UserPathHeader:         strings.TrimSpace(values.UserPathHeader),
		LicenseBadge:           strings.TrimSpace(values.LicenseBadge),
		LicenseTrial:           strings.TrimSpace(values.LicenseTrial),
		LicenseExpiresAt:       strings.TrimSpace(values.LicenseExpiresAt),
		LicenseValidUntil:      strings.TrimSpace(values.LicenseValidUntil),
		LicenseNoticeDays:      strings.TrimSpace(values.LicenseNoticeDays),
		LicenseWarningDays:     strings.TrimSpace(values.LicenseWarningDays),
	}
}

func cloneDashboardRuntimeConfig(values DashboardConfigResponse) DashboardConfigResponse {
	return values
}

func cloneConfiguredProviders(configs []providers.SanitizedProviderConfig) []providers.SanitizedProviderConfig {
	if len(configs) == 0 {
		return nil
	}
	cloned := make([]providers.SanitizedProviderConfig, len(configs))
	for i := range configs {
		cloned[i] = configs[i]
		if len(configs[i].Models) > 0 {
			cloned[i].Models = append([]string(nil), configs[i].Models...)
		}
	}
	return cloned
}

var validIntervals = map[string]bool{
	"daily":   true,
	"weekly":  true,
	"monthly": true,
	"yearly":  true,
}

const (
	dashboardTimeZoneHeader = "X-GoModel-Timezone"
	defaultDashboardTZ      = "UTC"
	defaultDateRangeDays    = usage.DefaultDateRangeDays
	maxDateRangeDays        = usage.MaxDateRangeDays
)

var timeNow = time.Now

// parseUsageParams extracts UsageQueryParams from the request query string.
// Returns an error if date parameters are provided but malformed.
func parseUsageParams(c *echo.Context) (usage.UsageQueryParams, error) {
	params, err := parseDateRangeParams(c)
	if err != nil {
		return params, err
	}

	// Parse interval
	params.Interval = c.QueryParam("interval")
	if !validIntervals[params.Interval] {
		params.Interval = "daily"
	}
	params.CacheMode = c.QueryParam("cache_mode")
	// Multi-value filters repeat the parameter (model=a&model=b). Values that
	// cannot hold a comma also accept a comma-separated list, like endpoint
	// always has; labels and user paths may contain commas, so they do not.
	params.Models = queryValues(c, "model", true)
	params.Providers = queryValues(c, "provider", true)
	params.Labels = queryValues(c, "label", false)
	params.AuthKeyIDs = queryValues(c, "auth_key_id", true)
	params.Endpoints = queryValues(c, "endpoint", true)
	params.SessionID = strings.TrimSpace(c.QueryParam("session_id"))
	if raw := c.QueryParam("operation"); raw != "" {
		ops, unknown, ok := core.ParseOperations(raw)
		if !ok {
			return params, core.NewInvalidRequestError("invalid operation: "+unknown, nil)
		}
		params.Operations = ops
	}

	scope, userPaths, err := scopedUserPaths(c, "user_path", queryValues(c, "user_path", false))
	if err != nil {
		return params, err
	}
	params.UserPath = scope
	params.UserPaths = userPaths

	return params, nil
}

// queryValues returns every value of a repeated query parameter, trimmed,
// without blanks or duplicates. splitCommas also splits each value on commas.
func queryValues(c *echo.Context, name string, splitCommas bool) []string {
	var values []string
	seen := map[string]bool{}
	for _, raw := range c.QueryParams()[name] {
		parts := []string{raw}
		if splitCommas {
			parts = strings.Split(raw, ",")
		}
		for _, part := range parts {
			value := strings.TrimSpace(part)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func normalizeUserPathQueryParam(fieldName, raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", core.NewInvalidRequestError("invalid "+fieldName+": "+err.Error(), err)
	}
	return userPath, nil
}

// parseDateRangeParams extracts common date range query params.
// Returns an error if date parameters are provided but malformed.
func parseDateRangeParams(c *echo.Context) (usage.UsageQueryParams, error) {
	var params usage.UsageQueryParams

	timeZone, location := dashboardTimeZone(c)
	params.TimeZone = timeZone

	now := timeNow().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)

	days := defaultDateRangeDays
	if d := c.QueryParam("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = min(parsed, maxDateRangeDays)
		}
	}

	start, end, err := usage.BuildDateRange(strings.TrimSpace(c.QueryParam("start_date")), strings.TrimSpace(c.QueryParam("end_date")), days, location, today)
	if err != nil {
		return params, err
	}
	params.StartDate = start
	params.EndDate = end
	return params, nil
}

func dashboardTimeZone(c *echo.Context) (string, *time.Location) {
	value := strings.TrimSpace(c.Request().Header.Get(dashboardTimeZoneHeader))
	if value == "" {
		return defaultDashboardTZ, time.UTC
	}

	location, err := time.LoadLocation(value)
	if err != nil {
		return defaultDashboardTZ, time.UTC
	}

	return location.String(), location
}

// handleError converts errors to appropriate HTTP responses, matching the
// format used by the main API handlers in the server package.
func handleError(c *echo.Context, err error) error {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	if errors.Is(err, context.Canceled) {
		gatewayErr := core.NewInvalidRequestErrorWithStatus(statusClientClosedRequest, "request canceled", err).
			WithCode("request_canceled")
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		gatewayErr := core.NewInvalidRequestErrorWithStatus(http.StatusGatewayTimeout, "request timed out", err).
			WithCode("request_timeout")
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	fallback := &core.GatewayError{
		Type:       core.ErrorTypeInternal,
		Message:    "an unexpected error occurred",
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
	logHandledAdminError(c, fallback)
	return c.JSON(fallback.HTTPStatusCode(), fallback.ToJSON())
}

func logHandledAdminError(c *echo.Context, gatewayErr *core.GatewayError) {
	if gatewayErr == nil {
		return
	}

	attrs := []any{
		"type", gatewayErr.Type,
		"status", gatewayErr.HTTPStatusCode(),
		"message", gatewayErr.Message,
	}
	if gatewayErr.Provider != "" {
		attrs = append(attrs, "provider", gatewayErr.Provider)
	}
	if gatewayErr.Param != nil {
		attrs = append(attrs, "param", *gatewayErr.Param)
	}
	if gatewayErr.Code != nil {
		attrs = append(attrs, "code", *gatewayErr.Code)
	}
	if gatewayErr.Err != nil {
		attrs = append(attrs, "error", gatewayErr.Err)
	}
	if c != nil && c.Request() != nil {
		req := c.Request()
		attrs = append(attrs,
			"method", req.Method,
			"path", req.URL.Path,
		)
		if requestID := requestIDFromAdminContextOrHeader(req); requestID != "" {
			attrs = append(attrs, "request_id", requestID)
		}
	}

	status := gatewayErr.HTTPStatusCode()
	if status == statusClientClosedRequest {
		slog.Debug("admin request canceled", attrs...)
		return
	}
	if status >= http.StatusInternalServerError {
		slog.Error("admin request failed", attrs...)
		return
	}
	slog.Warn("admin request failed", attrs...)
}

func requestIDFromAdminContextOrHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	if requestID := strings.TrimSpace(core.GetRequestID(req.Context())); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(req.Header.Get(core.RequestIDHeader))
}
