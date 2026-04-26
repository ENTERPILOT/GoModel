package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/config"
	"gomodel/internal/admin"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/providers"
)

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

func TestRefreshRuntime_SkipsDisabledModelOverrides(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&runtimeRefreshMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-test", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "openai", "openai")

	app := &App{
		config: &config.Config{},
		providers: &providers.InitResult{
			Registry: registry,
		},
		modelOverrides: &modeloverrides.Result{},
	}

	report, err := app.RefreshRuntime(context.Background())
	if err != nil {
		t.Fatalf("RefreshRuntime() error = %v", err)
	}

	step := runtimeRefreshStepByName(report.Steps, "model_overrides")
	if step == nil {
		t.Fatalf("model_overrides step missing: %+v", report.Steps)
		return
	}
	if step.Status != admin.RuntimeRefreshStatusSkipped {
		t.Fatalf("model_overrides step status = %q, want skipped; step=%+v", step.Status, *step)
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

func TestRuntimeWorkflowFeatureCaps_EnableFallbackFromOverride(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeOff,
			Overrides: map[string]config.FallbackModelOverride{
				"gpt-4o": {Mode: config.FallbackModeManual},
			},
		},
	}

	caps := runtimeWorkflowFeatureCaps(cfg)
	if !caps.Fallback {
		t.Fatal("runtimeWorkflowFeatureCaps().Fallback = false, want true")
	}
}

func TestDefaultWorkflowInput_SetsFallbackFeature(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeAuto,
		},
	}

	input := defaultWorkflowInput(cfg, nil, nil)
	if input.Payload.Features.Fallback == nil {
		t.Fatal("defaultWorkflowInput().Payload.Features.Fallback = nil, want non-nil")
	}
	if !*input.Payload.Features.Fallback {
		t.Fatal("defaultWorkflowInput().Payload.Features.Fallback = false, want true")
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

func TestDashboardRuntimeConfig_ExposesFallbackMode(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeManual,
		},
	}

	values := dashboardRuntimeConfig(cfg, false)
	if got := values.FeatureFallbackMode; got != string(config.FallbackModeManual) {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigFeatureFallbackMode, got, config.FallbackModeManual)
	}
}

func TestDashboardRuntimeConfig_InvalidFallbackModeDefaultsOff(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackMode("experimental"),
		},
	}

	values := dashboardRuntimeConfig(cfg, false)
	if got := values.FeatureFallbackMode; got != string(config.FallbackModeOff) {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigFeatureFallbackMode, got, config.FallbackModeOff)
	}
}

func TestDashboardRuntimeConfig_FallbackOverrideEnablesVisibilityWhenDefaultModeIsOff(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeOff,
			Overrides: map[string]config.FallbackModelOverride{
				"gpt-4o": {Mode: config.FallbackModeManual},
			},
		},
	}

	values := dashboardRuntimeConfig(cfg, false)
	if got := values.FeatureFallbackMode; got != string(config.FallbackModeManual) {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigFeatureFallbackMode, got, config.FallbackModeManual)
	}
}

func TestDashboardRuntimeConfig_ExposesFeatureAvailabilityFlags(t *testing.T) {
	semanticOff := false
	cfg := &config.Config{
		Logging: config.LogConfig{
			Enabled: true,
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

	values := dashboardRuntimeConfig(cfg, true)
	if got := values.LoggingEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLoggingEnabled, got)
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

	values := dashboardRuntimeConfig(cfg, false)
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
