package app

import (
	"testing"

	"gomodel/config"
	"gomodel/internal/admin"
	"gomodel/internal/guardrails"
)

func TestRuntimeExecutionFeatureCaps_EnableFallbackFromOverride(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeOff,
			Overrides: map[string]config.FallbackModelOverride{
				"gpt-4o": {Mode: config.FallbackModeManual},
			},
		},
	}

	caps := runtimeExecutionFeatureCaps(cfg)
	if !caps.Fallback {
		t.Fatal("runtimeExecutionFeatureCaps().Fallback = false, want true")
	}
}

func TestDefaultExecutionPlanInput_SetsFallbackFeature(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeAuto,
		},
	}

	input := defaultExecutionPlanInput(cfg, nil, nil)
	if input.Payload.Features.Fallback == nil {
		t.Fatal("defaultExecutionPlanInput().Payload.Features.Fallback = nil, want non-nil")
	}
	if !*input.Payload.Features.Fallback {
		t.Fatal("defaultExecutionPlanInput().Payload.Features.Fallback = false, want true")
	}
}

func TestDefaultExecutionPlanInput_IncludesConfiguredGuardrailsMissingFromLoadedCatalog(t *testing.T) {
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

	input := defaultExecutionPlanInput(cfg, nil, []guardrails.Definition{
		{Name: "policy-system", Type: "system_prompt"},
	})

	if !input.Payload.Features.Guardrails {
		t.Fatal("defaultExecutionPlanInput().Payload.Features.Guardrails = false, want true")
	}
	if len(input.Payload.Guardrails) != 1 {
		t.Fatalf("len(defaultExecutionPlanInput().Payload.Guardrails) = %d, want 1", len(input.Payload.Guardrails))
	}
	if got := input.Payload.Guardrails[0].Ref; got != "policy-system" {
		t.Fatalf("defaultExecutionPlanInput().Payload.Guardrails[0].Ref = %q, want policy-system", got)
	}
}

func TestDefaultExecutionPlanInput_TrimsConfiguredGuardrailRefs(t *testing.T) {
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

	input := defaultExecutionPlanInput(cfg, []string{"policy-system"}, nil)
	if len(input.Payload.Guardrails) != 1 {
		t.Fatalf("len(defaultExecutionPlanInput().Payload.Guardrails) = %d, want 1", len(input.Payload.Guardrails))
	}
	if got := input.Payload.Guardrails[0].Ref; got != "policy-system" {
		t.Fatalf("defaultExecutionPlanInput().Payload.Guardrails[0].Ref = %q, want policy-system", got)
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
