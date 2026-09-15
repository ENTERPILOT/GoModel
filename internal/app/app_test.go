package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
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

	require.Equal(t, "smart", selector.outcome.Source)
	require.Equal(t, "session-a", selector.outcome.SessionID)
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
	require.NotNil(t, sub)

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
		require.True(t, closed)

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
	require.NoError(t, err)
	require.Equal(t, admin.RuntimeRefreshStatusOK, report.Status, "RefreshRuntime().Status = %q, want ok; steps=%+v", report.Status, report.Steps)
	require.Equal(t, 1, report.ModelCount)
	require.Equal(t, 1, report.ProviderCount)

	info := registry.GetModel("openai/gpt-test")
	require.NotNil(t, info)
	require.NotNil(t, info.Model.Metadata)
	require.Equal(t, "GPT Test", info.Model.Metadata.DisplayName)
	require.NotNil(t, info.Model.Metadata.ContextWindow)
	require.Equal(t, 128000, *info.Model.Metadata.ContextWindow)
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
	require.NoError(t, err)

	step := runtimeRefreshStepByName(report.Steps, "virtual_models")
	require.NotNil(t, step, "virtual_models step missing: %+v", report.Steps)
	require.Equal(t, admin.RuntimeRefreshStatusSkipped, step.Status, "virtual_models step status = %q, want skipped; step=%+v", step.Status, *step)
}

func TestRefreshRuntime_ReturnsGatewayErrorWhenContextCanceledBeforeAcquire(t *testing.T) {
	app := &App{}
	ch := app.runtimeRefreshSemaphore()
	ch <- struct{}{}
	defer func() { <-ch }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := app.RefreshRuntime(ctx)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusRequestTimeout, gatewayErr.HTTPStatusCode())
	require.Equal(t, "runtime_refresh", gatewayErr.Provider)
}

func TestRunRuntimeRefreshStepReturnsContextErrorWithoutAppendingStep(t *testing.T) {
	app := &App{}
	report := admin.RuntimeRefreshReport{}

	err := app.runRuntimeRefreshStep(&report, "providers", func() runtimeRefreshStepResult {
		return runtimeRefreshStepResult{err: context.Canceled}
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, report.Steps)
}

func TestProviderRefreshIssueCountIncludesAvailabilityErrors(t *testing.T) {
	got := providerRefreshIssueCount([]providers.ProviderRuntimeSnapshot{
		{Name: "healthy"},
		{Name: "model-fetch", LastModelFetchError: " failed to fetch models "},
		{Name: "availability", LastAvailabilityError: " provider unavailable "},
		{Name: "both", LastModelFetchError: "fetch failed", LastAvailabilityError: "unavailable"},
	})
	require.Equal(t, 3, got)
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
	require.True(t, caps.Failover)
}

func TestDefaultWorkflowInput_SetsFailoverFeature(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
	}

	input := defaultWorkflowInput(cfg, nil, nil)
	require.NotNil(t, input.Payload.Features.Failover)
	require.True(t, *input.Payload.Features.Failover)
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

	require.True(t, input.Payload.Features.Guardrails)
	require.Len(t, input.Payload.Steps, 1)
	got := input.Payload.Steps[0].Ref
	require.Equal(t, "policy-system", got)
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
	require.Len(t, input.Payload.Steps, 1)
	got := input.Payload.Steps[0].Ref
	require.Equal(t, "policy-system", got)
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
	}, testPluginCatalog(t))
	require.NoError(t, err)
	require.Empty(t, definitions)
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
	}, testPluginCatalog(t))
	require.Error(t, err)
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
	}, testPluginCatalog(t))
	require.NoError(t, err)
	require.Len(t, definitions, 1)
	require.Equal(t, "policy-system", definitions[0].Name)
	require.Equal(t, "system_prompt", definitions[0].Type)
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
	}, testPluginCatalog(t))
	require.Error(t, err)

	_, err = configGuardrailDefinitions(config.GuardrailsConfig{
		Enabled: true,
		Rules: []config.GuardrailRuleConfig{
			{
				Name: "policy-system",
				Type: "   ",
			},
		},
	}, testPluginCatalog(t))
	require.Error(t, err)
}

func TestDashboardRuntimeConfig_ExposesFailoverEnabled(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	got := values.FailoverEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigFailoverEnabled, got)
}

func TestDashboardRuntimeConfig_ExposesDemoMode(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{}, false, true, false)
	got := values.DemoMode
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigDemoMode, got)
}

func TestDashboardRuntimeConfig_FailoverDisabled(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: false,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	got := values.FailoverEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigFailoverEnabled, got)
}

func TestDashboardRuntimeConfig_DefaultModeDoesNotEnableFailover(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled:     false,
			DefaultMode: config.FailoverModeManual,
		},
	}

	values := dashboardRuntimeConfig(cfg, false, false, false)
	got := values.FailoverEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigFailoverEnabled, got)
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
	got := values.LoggingEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLoggingEnabled, got)
	got = values.LoggingRetentionDays
	require.Equal(t, "14", got, "dashboardRuntimeConfig()[%q] = %q, want 14", admin.DashboardConfigLoggingRetentionDays, got)
	got = values.UsageEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigUsageEnabled, got)
	got = values.BudgetsEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigBudgetsEnabled, got)
	got = values.GuardrailsEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigGuardrailsEnabled, got)
	got = // Guardrails imply the plugin system, so the Plugins page shows too.
		values.PluginsEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigPluginsEnabled, got)
	got = values.CacheEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigCacheEnabled, got)
	got = values.RedisURL
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigRedisURL, got)
	got = values.SemanticCacheEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigSemanticCacheEnabled, got)
	got = values.LiveLogsEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLiveLogsEnabled, got)
	got = values.MCPEnabled
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigMCPEnabled, got)
}

func TestDashboardRuntimeConfig_ExposesIndefiniteLoggingRetention(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{}, false, false, false)
	got := values.LoggingRetentionDays
	require.Equal(t, "0", got, "dashboardRuntimeConfig()[%q] = %q, want 0", admin.DashboardConfigLoggingRetentionDays, got)
}

func TestDashboardRuntimeConfig_HidesMCPWhenDisabled(t *testing.T) {
	values := dashboardRuntimeConfig(&config.Config{
		MCP: config.MCPConfig{Enabled: false},
	}, false, false, false)
	got := values.MCPEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigMCPEnabled, got)
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
			got := values.VirtualModelStrategies
			require.Equal(t, tt.want, got, "dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigVMStrategies, got, tt.want)
		})
	}
}

func TestDashboardRuntimeConfig_ExposesUserPathHeader(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{name: "nil config falls back to default", cfg: nil, want: "X-GoModel-User-Path"},
		{name: "unset header falls back to default", cfg: &config.Config{}, want: "X-GoModel-User-Path"},
		{
			name: "custom header is canonicalized",
			cfg:  &config.Config{Server: config.ServerConfig{UserPathHeader: "x-tenant-path"}},
			want: "X-Tenant-Path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := dashboardRuntimeConfig(tt.cfg, false, false, false)
			got := values.UserPathHeader
			require.Equal(t, tt.want, got, "dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigUserPathHeader, got, tt.want)
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
	got := values.UsageEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigUsageEnabled, got)
	got = values.CacheEnabled
	require.Equal(t, "off", got, "dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigCacheEnabled, got)
	got = values.RedisURL
	require.Equal(t, "on", got, "dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigRedisURL, got)
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
			got := usagePricingRecalculationConfigured(test.cfg)
			require.Equal(t, test.want, got)
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

	require.Len(t, serverCfg.RequestRewriters, 1)
	assert.Equal(t, "r1", serverCfg.RequestRewriters[0].Name())
	assert.Len(t, serverCfg.OuterMiddleware, 1)
	assert.Len(t, serverCfg.ExtraMiddleware, 1)
	assert.Len(t, serverCfg.ExtraRoutes, 1)
	assert.Len(t, serverCfg.ExtraAuthSkipPaths, 2)
	assert.Len(t, serverCfg.RequestAuthenticators, 1)

	// A nil registry must leave the config untouched.
	empty := &server.Config{}
	applyExtensions(empty, nil)
	assert.Nil(t, empty.RequestRewriters)
	assert.Nil(t, empty.OuterMiddleware)
	assert.Nil(t, empty.ExtraMiddleware)
	assert.Nil(t, empty.ExtraRoutes)
	assert.Nil(t, empty.ExtraAuthSkipPaths)
	assert.Nil(t, empty.RequestAuthenticators)
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
	require.False(t, hasUsableRequestAuthenticator(nil))

	registry := &ext.Registry{}
	var typedNil *appTestAuthenticator
	registry.RegisterAuthenticator(typedNil)
	require.False(t, hasUsableRequestAuthenticator(registry))

	authenticator := &recorderAwareAppAuthenticator{}
	registry.RegisterAuthenticator(authenticator)
	require.True(t, hasUsableRequestAuthenticator(registry))

	recorder := &appAuthenticationEventRecorder{}
	bindAuthenticationEventRecorders(registry, recorder)
	require.Same(t, recorder, authenticator.recorder)
}

func TestLogStartupInfoTreatsExtensionAuthenticatorAsEffectiveAuth(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	app := &App{config: &config.Config{}, extensionAuth: true}
	app.logStartupInfo()
	output := logs.String()
	require.NotContains(t, output, "UNSAFE MODE")
	require.NotContains(t, output, "unauthenticated access allowed", "extension-authenticated startup emitted unsafe warning: %s", output)
	require.Contains(t, output, `mode=extension`)
}

type staticRewriter struct{ name string }

func (r *staticRewriter) Name() string { return r.name }

func (r *staticRewriter) Rewrite(context.Context, ext.Input) (*ext.Result, error) {
	return nil, nil
}

// testPluginCatalog returns a catalog of the built-in plugins.
func testPluginCatalog(t *testing.T) *plugins.Catalog {
	t.Helper()
	catalog := plugins.NewCatalog()
	for _, factory := range builtin.All() {
		err := catalog.Register(factory, plugins.SourceBuiltin)
		require.NoError(t, err)
	}
	return catalog
}

func TestDashboardRuntimeConfig_PluginsFlag(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"default off", &config.Config{}, "off"},
		{"plugins on", &config.Config{Plugins: config.PluginsConfig{Enabled: true}}, "on"},
		{"guardrails imply plugins", &config.Config{Guardrails: config.GuardrailsConfig{Enabled: true}}, "on"},
	} {
		got := dashboardRuntimeConfig(tt.cfg, false, false, false).PluginsEnabled
		assert.Equal(t, tt.want, got, "%s: PLUGINS_ENABLED = %q, want %q", tt.name, got, tt.want)
	}
}
