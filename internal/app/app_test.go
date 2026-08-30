package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/server"
)

type routeObservationSelector struct {
	outcome ext.RouteOutcome
}

func (*routeObservationSelector) Name() string                           { return "observer" }
func (*routeObservationSelector) Select(ext.RouteRequest) (string, bool) { return "", false }
func (*routeObservationSelector) OnAttemptStart(ext.RouteTarget)         {}
func (s *routeObservationSelector) OnAttemptEnd(outcome ext.RouteOutcome) {
	s.outcome = outcome
}

func TestRouteSelectorHooksExposeSuccessfulRouteAffinityContext(t *testing.T) {
	selector := &routeObservationSelector{}
	hooks := routeSelectorHooks(selector)
	ctx := core.WithSessionID(context.Background(), "session-a")
	ctx = core.WithWorkflow(ctx, &core.Workflow{Resolution: &core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("smart", ""),
		ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt"},
		AliasApplied:     true,
	}})
	ctx = hooks.OnRequestStart(ctx, llmclient.RequestInfo{Provider: "openai", Model: "gpt"})
	hooks.OnRequestEnd(ctx, llmclient.ResponseInfo{Provider: "openai", Model: "gpt", StatusCode: http.StatusOK})

	if selector.outcome.Source != "smart" || selector.outcome.SessionID != "session-a" {
		t.Fatalf("route affinity context = %q/%q, want smart/session-a",
			selector.outcome.Source, selector.outcome.SessionID)
	}
}

type runtimeRefreshMockProvider struct {
	models *core.ModelsResponse
	err    error
}

func (m *runtimeRefreshMockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (m *runtimeRefreshMockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (m *runtimeRefreshMockProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.models, nil
}

func (m *runtimeRefreshMockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (m *runtimeRefreshMockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (m *runtimeRefreshMockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("not supported", nil)
}

func TestShutdownClosesLiveStreamsBeforeWaitingForServer(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true})
	sub := broker.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	stopped := make(chan struct{})
	serverDone := make(chan error)
	subscriberClosed := make(chan bool, 1)

	app := &App{
		live: broker,
		serverStop: func() {
			close(stopped)
		},
		serverDone: serverDone,
	}

	go func() {
		<-stopped
		_, ok := <-sub.Events
		subscriberClosed <- !ok
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		broker.Close()
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case closed := <-subscriberClosed:
		if !closed {
			t.Fatal("live subscriber remained open")
		}
	default:
		t.Fatal("server stopped before live subscriber closure was observed")
	}
}

func TestRefreshRuntime_RefreshesModelListProvidersAndRegistryCache(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&runtimeRefreshMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-test", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "openai", "openai")

	modelListServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"version": 1,
			"updated_at": "2026-04-11T00:00:00Z",
			"providers": {
				"openai": {
					"display_name": "OpenAI",
					"api_type": "openai",
					"supported_modes": ["chat"]
				}
			},
			"models": {
				"gpt-test": {
					"display_name": "GPT Test",
					"modes": ["chat"],
					"context_window": 128000
				}
			},
			"provider_models": {}
		}`))
	}))
	defer modelListServer.Close()

	app := &App{
		config: &config.Config{
			Cache: config.CacheConfig{
				Model: config.ModelCacheConfig{
					ModelList: config.ModelListConfig{URL: modelListServer.URL},
				},
			},
		},
		providers: &providers.InitResult{Registry: registry},
	}

	report, err := app.RefreshRuntime(context.Background())
	if err != nil {
		t.Fatalf("RefreshRuntime() error = %v", err)
	}
	if report.Status != admin.RuntimeRefreshStatusOK {
		t.Fatalf("RefreshRuntime().Status = %q, want ok; steps=%+v", report.Status, report.Steps)
	}
	if report.ModelCount != 1 || report.ProviderCount != 1 {
		t.Fatalf("RefreshRuntime() counts = %d/%d, want 1/1", report.ModelCount, report.ProviderCount)
	}

	info := registry.GetModel("openai/gpt-test")
	if info == nil || info.Model.Metadata == nil {
		t.Fatal("expected refreshed provider model metadata")
	}
	if info.Model.Metadata.DisplayName != "GPT Test" {
		t.Fatalf("DisplayName = %q, want GPT Test", info.Model.Metadata.DisplayName)
	}
	if info.Model.Metadata.ContextWindow == nil || *info.Model.Metadata.ContextWindow != 128000 {
		t.Fatalf("ContextWindow = %v, want 128000", info.Model.Metadata.ContextWindow)
	}
}

func TestRefreshRuntime_SkipsDisabledVirtualModels(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&runtimeRefreshMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-test", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "openai", "openai")

	// virtualModels is left nil so the virtual_models refresh step reports
	// skipped, which is what this test asserts.
	app := &App{
		config: &config.Config{},
		providers: &providers.InitResult{
			Registry: registry,
		},
	}

	report, err := app.RefreshRuntime(context.Background())
	if err != nil {
		t.Fatalf("RefreshRuntime() error = %v", err)
	}

	step := runtimeRefreshStepByName(report.Steps, "virtual_models")
	if step == nil {
		t.Fatalf("virtual_models step missing: %+v", report.Steps)
		return
	}
	if step.Status != admin.RuntimeRefreshStatusSkipped {
		t.Fatalf("virtual_models step status = %q, want skipped; step=%+v", step.Status, *step)
	}
}

func TestRefreshRuntime_ReturnsGatewayErrorWhenContextCanceledBeforeAcquire(t *testing.T) {
	app := &App{}
	ch := app.runtimeRefreshSemaphore()
	ch <- struct{}{}
	defer func() { <-ch }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := app.RefreshRuntime(ctx)
	if err == nil {
		t.Fatal("RefreshRuntime() error = nil, want cancellation error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("RefreshRuntime() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", gatewayErr.HTTPStatusCode())
	}
	if gatewayErr.Provider != "runtime_refresh" {
		t.Fatalf("provider = %q, want runtime_refresh", gatewayErr.Provider)
	}
}

func TestRunRuntimeRefreshStepReturnsContextErrorWithoutAppendingStep(t *testing.T) {
	app := &App{}
	report := admin.RuntimeRefreshReport{}

	err := app.runRuntimeRefreshStep(&report, "providers", func() runtimeRefreshStepResult {
		return runtimeRefreshStepResult{err: context.Canceled}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runRuntimeRefreshStep() error = %v, want context canceled", err)
	}
	if len(report.Steps) != 0 {
		t.Fatalf("steps = %+v, want none appended for context cancellation", report.Steps)
	}
}

func TestProviderRefreshIssueCountIncludesAvailabilityErrors(t *testing.T) {
	got := providerRefreshIssueCount([]providers.ProviderRuntimeSnapshot{
		{Name: "healthy"},
		{Name: "model-fetch", LastModelFetchError: " failed to fetch models "},
		{Name: "availability", LastAvailabilityError: " provider unavailable "},
		{Name: "both", LastModelFetchError: "fetch failed", LastAvailabilityError: "unavailable"},
	})
	if got != 3 {
		t.Fatalf("providerRefreshIssueCount() = %d, want 3", got)
	}
}

func runtimeRefreshStepByName(steps []admin.RuntimeRefreshStep, name string) *admin.RuntimeRefreshStep {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i]
		}
	}
	return nil
}

func TestRuntimeWorkflowFeatureCaps_EnableFailoverFromExplicitFlag(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
	}

	caps := runtimeWorkflowFeatureCaps(cfg)
	if !caps.Failover {
		t.Fatal("runtimeWorkflowFeatureCaps().Failover = false, want true")
	}
}

func TestDefaultWorkflowInput_SetsFailoverFeature(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
	}

	input := defaultWorkflowInput(cfg, nil, nil)
	if input.Payload.Features.Failover == nil {
		t.Fatal("defaultWorkflowInput().Payload.Features.Failover = nil, want non-nil")
	}
	if !*input.Payload.Features.Failover {
		t.Fatal("defaultWorkflowInput().Payload.Features.Failover = false, want true")
	}
}

func TestDefaultWorkflowInput_IncludesConfiguredGuardrailsMissingFromLoadedCatalog(t *testing.T) {
	cfg := &config.Config{
		Guardrails: config.GuardrailsConfig{
			Enabled: true,
			Rules: []config.GuardrailRuleConfig{
				{
					Name:  "policy-system",
					Type:  "system_prompt",
					Order: 10,
				},
			},
		},
	}

	input := defaultWorkflowInput(cfg, nil, []guardrails.Definition{
		{Name: "policy-system", Type: "system_prompt"},
	})

	if !input.Payload.Features.Guardrails {
		t.Fatal("defaultWorkflowInput().Payload.Features.Guardrails = false, want true")
	}
	if len(input.Payload.Guardrails) != 1 {
		t.Fatalf("len(defaultWorkflowInput().Payload.Guardrails) = %d, want 1", len(input.Payload.Guardrails))
	}
	if got := input.Payload.Guardrails[0].Ref; got != "policy-system" {
		t.Fatalf("defaultWorkflowInput().Payload.Guardrails[0].Ref = %q, want policy-system", got)
	}
}

func TestDefaultWorkflowInput_TrimsConfiguredGuardrailRefs(t *testing.T) {
	cfg := &config.Config{
		Guardrails: config.GuardrailsConfig{
			Enabled: true,
			Rules: []config.GuardrailRuleConfig{
				{
					Name:  "  policy-system  ",
					Type:  "system_prompt",
					Order: 10,
				},
			},
		},
	}

	input := defaultWorkflowInput(cfg, []string{"policy-system"}, nil)
	if len(input.Payload.Guardrails) != 1 {
		t.Fatalf("len(defaultWorkflowInput().Payload.Guardrails) = %d, want 1", len(input.Payload.Guardrails))
	}
	if got := input.Payload.Guardrails[0].Ref; got != "policy-system" {
		t.Fatalf("defaultWorkflowInput().Payload.Guardrails[0].Ref = %q, want policy-system", got)
	}
}

func TestConfigGuardrailDefinitions_DisabledIgnoresInvalidRules(t *testing.T) {
	definitions, err := configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: false,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "draft-rule",
				Type: "future_guardrail_type",
				SystemPrompt: config.SystemPromptSettings{
					Content: "",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("configGuardrailDefinitions() error = %v, want nil", err)
	}
	if len(definitions) != 0 {
		t.Fatalf("len(configGuardrailDefinitions()) = %d, want 0", len(definitions))
	}
}

func TestConfigGuardrailDefinitions_EnabledRejectsUnknownType(t *testing.T) {
	_, err := configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: true,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "draft-rule",
				Type: "future_guardrail_type",
			},
		},
	})
	if err == nil {
		t.Fatal("configGuardrailDefinitions() error = nil, want unsupported type error")
	}
}

func TestConfigGuardrailDefinitions_TrimAndCanonicalizeRuleIdentity(t *testing.T) {
	definitions, err := configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: true,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "  policy-system  ",
				Type: "  SYSTEM_PROMPT  ",
				SystemPrompt: config.SystemPromptSettings{
					Mode:    "inject",
					Content: "be precise",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("configGuardrailDefinitions() error = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("len(configGuardrailDefinitions()) = %d, want 1", len(definitions))
	}
	if definitions[0].Name != "policy-system" {
		t.Fatalf("definitions[0].Name = %q, want policy-system", definitions[0].Name)
	}
	if definitions[0].Type != "system_prompt" {
		t.Fatalf("definitions[0].Type = %q, want system_prompt", definitions[0].Type)
	}
}

func TestConfigGuardrailDefinitions_RejectsBlankNameOrType(t *testing.T) {
	_, err := configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: true,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "   ",
				Type: "system_prompt",
			},
		},
	})
	if err == nil {
		t.Fatal("configGuardrailDefinitions() error = nil, want name validation error")
	}

	_, err = configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: true,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "policy-system",
				Type: "   ",
			},
		},
	})
	if err == nil {
		t.Fatal("configGuardrailDefinitions() error = nil, want type validation error")
	}
}

func TestDashboardRuntimeConfig_ExposesFailoverEnabled(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	if got := values.FailoverEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigFailoverEnabled, got)
	}
}

func TestDashboardRuntimeConfig_ExposesDemoMode(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{}, false, true, false)
	if got := values.DemoMode; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigDemoMode, got)
	}
}

func TestDashboardRuntimeConfig_FailoverDisabled(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: false,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	if got := values.FailoverEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigFailoverEnabled, got)
	}
}

func TestDashboardRuntimeConfig_DefaultModeDoesNotEnableFailover(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled:     false,
			DefaultMode: config.FailoverModeManual,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	if got := values.FailoverEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigFailoverEnabled, got)
	}
}

func TestDashboardRuntimeConfig_ExposesFeatureAvailabilityFlags(t *testing.T) {
	semanticOff := false
	cfg := &config.Config{
		Logging: config.LogConfig{
			Enabled:       true,
			RetentionDays: 14,
		},
		Usage: config.UsageConfig{
			Enabled: true,
		},
		Budgets: config.BudgetsConfig{
			Enabled: true,
		},
		Guardrails: config.GuardrailsConfig{
			Enabled: true,
		},
		Admin: config.AdminConfig{
			LiveLogsEnabled: true,
		},
		MCP: config.MCPConfig{
			Enabled: true,
		},
		Cache: config.CacheConfig{
			Response: config.ResponseCacheConfig{
				Simple: &config.SimpleCacheConfig{
					Redis: &config.RedisResponseConfig{
						URL: "redis://localhost:6379",
					},
				},
				Semantic: &config.SemanticCacheConfig{Enabled: &semanticOff},
			},
		},
	}

	values := dashboardRuntimeConfig(cfg, true, false, false)
	if got := values.LoggingEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLoggingEnabled, got)
	}
	if got := values.LoggingRetentionDays; got != "14" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want 14", admin.DashboardConfigLoggingRetentionDays, got)
	}
	if got := values.UsageEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigUsageEnabled, got)
	}
	if got := values.BudgetsEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigBudgetsEnabled, got)
	}
	if got := values.GuardrailsEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigGuardrailsEnabled, got)
	}
	if got := values.CacheEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigCacheEnabled, got)
	}
	if got := values.RedisURL; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigRedisURL, got)
	}
	if got := values.SemanticCacheEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigSemanticCacheEnabled, got)
	}
	if got := values.LiveLogsEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLiveLogsEnabled, got)
	}
	if got := values.MCPEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigMCPEnabled, got)
	}
}

func TestDashboardRuntimeConfig_ExposesIndefiniteLoggingRetention(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{}, false, false, false)
	if got := values.LoggingRetentionDays; got != "0" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want 0", admin.DashboardConfigLoggingRetentionDays, got)
	}
}

func TestDashboardRuntimeConfig_HidesMCPWhenDisabled(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{
		MCP: config.MCPConfig{Enabled: false},
	}, false, false, false)
	if got := values.MCPEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigMCPEnabled, got)
	}
}

func TestDashboardRuntimeConfig_VirtualModelStrategies(t *testing.T) {
	tests := []struct {
		name            string
		adaptiveRouting bool
		want            string
	}{
		{name: "core strategies without a route selector", adaptiveRouting: false, want: "round_robin,cost,failover"},
		{name: "adaptive offered with a route selector", adaptiveRouting: true, want: "round_robin,cost,failover,adaptive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := dashboardRuntimeConfig(&config.Config{}, false, false, tt.adaptiveRouting)
			if got := values.VirtualModelStrategies; got != tt.want {
				t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigVMStrategies, got, tt.want)
			}
		})
	}
}

func TestDashboardRuntimeConfig_HidesCacheAnalyticsWhenUsageDisabled(t *testing.T) {
	cfg := &config.Config{
		Usage: config.UsageConfig{
			Enabled: false,
		},
		Cache: config.CacheConfig{
			Response: config.ResponseCacheConfig{
				Simple: &config.SimpleCacheConfig{
					Redis: &config.RedisResponseConfig{
						URL: "redis://localhost:6379",
					},
				},
			},
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	if got := values.UsageEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigUsageEnabled, got)
	}
	if got := values.CacheEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigCacheEnabled, got)
	}
	if got := values.RedisURL; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigRedisURL, got)
	}
}

func TestUsagePricingRecalculationConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "enabled",
			cfg: &config.Config{
				Usage: config.UsageConfig{
					Enabled:                     true,
					PricingRecalculationEnabled: true,
				},
			},
			want: true,
		},
		{
			name: "disabled by usage",
			cfg: &config.Config{
				Usage: config.UsageConfig{
					Enabled:                     false,
					PricingRecalculationEnabled: true,
				},
			},
		},
		{
			name: "disabled by pricing switch",
			cfg: &config.Config{
				Usage: config.UsageConfig{
					Enabled:                     true,
					PricingRecalculationEnabled: false,
				},
			},
		},
		{
			name: "nil config",
			cfg:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := usagePricingRecalculationConfigured(test.cfg); got != test.want {
				t.Fatalf("usagePricingRecalculationConfigured() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestApplyExtensionsSnapshotsRegistryIntoServerConfig(t *testing.T) {
	reg := &ext.Registry{}
	reg.RegisterRewriter(&staticRewriter{name: "r1"})
	reg.UseOuterMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	reg.UseMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	reg.RegisterRoutes(func(_ *echo.Echo) {})
	reg.AddPublicPaths("/sso/callback", "/sso/*")
	reg.RegisterAuthenticator(&appTestAuthenticator{})

	serverCfg := &server.Config{}
	applyExtensions(serverCfg, reg)

	if len(serverCfg.RequestRewriters) != 1 || serverCfg.RequestRewriters[0].Name() != "r1" {
		t.Errorf("RequestRewriters not copied: %+v", serverCfg.RequestRewriters)
	}
	if len(serverCfg.OuterMiddleware) != 1 {
		t.Errorf("OuterMiddleware not copied: %d entries", len(serverCfg.OuterMiddleware))
	}
	if len(serverCfg.ExtraMiddleware) != 1 {
		t.Errorf("ExtraMiddleware not copied: %d entries", len(serverCfg.ExtraMiddleware))
	}
	if len(serverCfg.ExtraRoutes) != 1 {
		t.Errorf("ExtraRoutes not copied: %d entries", len(serverCfg.ExtraRoutes))
	}
	if len(serverCfg.ExtraAuthSkipPaths) != 2 {
		t.Errorf("ExtraAuthSkipPaths not copied: %v", serverCfg.ExtraAuthSkipPaths)
	}
	if len(serverCfg.RequestAuthenticators) != 1 {
		t.Errorf("RequestAuthenticators not copied: %v", serverCfg.RequestAuthenticators)
	}

	// A nil registry must leave the config untouched.
	empty := &server.Config{}
	applyExtensions(empty, nil)
	if empty.RequestRewriters != nil || empty.OuterMiddleware != nil || empty.ExtraMiddleware != nil || empty.ExtraRoutes != nil || empty.ExtraAuthSkipPaths != nil || empty.RequestAuthenticators != nil {
		t.Error("nil registry must not modify server config")
	}
}

type appTestAuthenticator struct{}

func (*appTestAuthenticator) Name() string { return "test" }

func (*appTestAuthenticator) AuthenticateRequest(context.Context, *http.Request) (*ext.Authentication, error) {
	return nil, nil
}

type recorderAwareAppAuthenticator struct {
	recorder ext.AuthenticationEventRecorder
}

func (*recorderAwareAppAuthenticator) Name() string { return "recorder-aware" }

func (*recorderAwareAppAuthenticator) AuthenticateRequest(context.Context, *http.Request) (*ext.Authentication, error) {
	return nil, nil
}

func (a *recorderAwareAppAuthenticator) SetAuthenticationEventRecorder(recorder ext.AuthenticationEventRecorder) {
	a.recorder = recorder
}

type appAuthenticationEventRecorder struct{}

func (*appAuthenticationEventRecorder) RecordAuthenticationEvent(ext.AuthenticationEvent) {}

func TestExtensionAuthenticationDetectionAndRecorderBinding(t *testing.T) {
	if hasUsableRequestAuthenticator(nil) {
		t.Fatal("nil registry reported extension authentication")
	}

	registry := &ext.Registry{}
	var typedNil *appTestAuthenticator
	registry.RegisterAuthenticator(typedNil)
	if hasUsableRequestAuthenticator(registry) {
		t.Fatal("typed-nil authenticator reported extension authentication")
	}

	authenticator := &recorderAwareAppAuthenticator{}
	registry.RegisterAuthenticator(authenticator)
	if !hasUsableRequestAuthenticator(registry) {
		t.Fatal("usable authenticator was not detected")
	}
	recorder := &appAuthenticationEventRecorder{}
	bindAuthenticationEventRecorders(registry, recorder)
	if authenticator.recorder != recorder {
		t.Fatal("authentication event recorder was not installed")
	}
}

func TestLogStartupInfoTreatsExtensionAuthenticatorAsEffectiveAuth(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	app := &App{config: &config.Config{}, extensionAuth: true}
	app.logStartupInfo()
	output := logs.String()
	if strings.Contains(output, "UNSAFE MODE") || strings.Contains(output, "unauthenticated access allowed") {
		t.Fatalf("extension-authenticated startup emitted unsafe warning: %s", output)
	}
	if !strings.Contains(output, `mode=extension`) {
		t.Fatalf("extension authentication mode was not logged: %s", output)
	}
}

type staticRewriter struct{ name string }

func (r *staticRewriter) Name() string { return r.name }

func (r *staticRewriter) Rewrite(context.Context, ext.Input) (*ext.Result, error) {
	return nil, nil
}
