package app

import (
	"testing"

	"gomodel/config"
	"gomodel/internal/admin"
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

	input := defaultExecutionPlanInput(cfg)
	if input.Payload.Features.Fallback == nil {
		t.Fatal("defaultExecutionPlanInput().Payload.Features.Fallback = nil, want non-nil")
	}
	if !*input.Payload.Features.Fallback {
		t.Fatal("defaultExecutionPlanInput().Payload.Features.Fallback = false, want true")
	}
}

func TestDashboardRuntimeConfig_ExposesFallbackMode(t *testing.T) {
	cfg := &config.Config{
		Fallback: config.FallbackConfig{
			DefaultMode: config.FallbackModeManual,
		},
	}

	values := dashboardRuntimeConfig(cfg)
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

	values := dashboardRuntimeConfig(cfg)
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

	values := dashboardRuntimeConfig(cfg)
	if got := values.FeatureFallbackMode; got != string(config.FallbackModeManual) {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want %q", admin.DashboardConfigFeatureFallbackMode, got, config.FallbackModeManual)
	}
}

func TestDashboardRuntimeConfig_ExposesFeatureAvailabilityFlags(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LogConfig{
			Enabled: true,
		},
		Usage: config.UsageConfig{
			Enabled: false,
		},
		Guardrails: config.GuardrailsConfig{
			Enabled: true,
		},
		Cache: config.CacheConfig{
			Response: config.ResponseCacheConfig{
				Simple: config.SimpleCacheConfig{
					Redis: &config.RedisResponseConfig{
						URL: "redis://localhost:6379",
					},
				},
				Semantic: config.SemanticCacheConfig{
					Enabled: false,
				},
			},
		},
	}

	values := dashboardRuntimeConfig(cfg)
	if got := values.LoggingEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigLoggingEnabled, got)
	}
	if got := values.UsageEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigUsageEnabled, got)
	}
	if got := values.GuardrailsEnabled; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigGuardrailsEnabled, got)
	}
	if got := values.RedisURL; got != "on" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want on", admin.DashboardConfigRedisURL, got)
	}
	if got := values.SemanticCacheEnabled; got != "off" {
		t.Fatalf("dashboardRuntimeConfig()[%q] = %q, want off", admin.DashboardConfigSemanticCacheEnabled, got)
	}
}
