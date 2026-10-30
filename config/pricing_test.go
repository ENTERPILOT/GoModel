package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPricingDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		assert.False(t, result.Config.Pricing.MarkupsEnabled, "markups are opt-in")
		assert.Empty(t, result.Config.Pricing.Markups)
		assert.True(t, result.Config.Pricing.BillCacheHits)
	})
}

func TestLoadPricingFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
pricing:
  markups_enabled: true
  bill_cache_hits: false
  markups:
    "/": 15
    "openai/": 10
    "anthropic/claude-opus-4-1": 25.5
`
		result := loadConfigYAML(t, dir, yamlConfig)

		assert.True(t, result.Config.Pricing.MarkupsEnabled)
		assert.False(t, result.Config.Pricing.BillCacheHits)
		assert.Equal(t, map[string]float64{
			"/":                         15,
			"openai/":                   10,
			"anthropic/claude-opus-4-1": 25.5,
		}, result.Config.Pricing.Markups)
	})
}

func TestLoadPricingMarkupsEnabledEnvOverridesYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		t.Setenv("PRICING_MARKUPS_ENABLED", "true")
		result := loadConfigYAML(t, dir, "pricing:\n  markups_enabled: false\n")
		assert.True(t, result.Config.Pricing.MarkupsEnabled)
	})
	withTempDir(t, func(dir string) {
		t.Setenv("PRICING_MARKUPS_ENABLED", "false")
		result := loadConfigYAML(t, dir, "pricing:\n  markups_enabled: true\n")
		assert.False(t, result.Config.Pricing.MarkupsEnabled)
	})
}

func TestLoadPricingBillCacheHitsEnvOverridesYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		t.Setenv("PRICING_BILL_CACHE_HITS", "true")
		result := loadConfigYAML(t, dir, "pricing:\n  bill_cache_hits: false\n")
		assert.True(t, result.Config.Pricing.BillCacheHits)
	})
	withTempDir(t, func(string) {
		t.Setenv("PRICING_BILL_CACHE_HITS", "false")
		result, err := Load()
		require.NoError(t, err)
		assert.False(t, result.Config.Pricing.BillCacheHits)
	})
}

func TestValidatePricingConfig(t *testing.T) {
	tests := []struct {
		name    string
		markups map[string]float64
		wantErr string
	}{
		{name: "valid", markups: map[string]float64{"/": 0, "openai/": 12.5}},
		{name: "negative", markups: map[string]float64{"/": -5}, wantErr: "must be a finite number >= 0"},
		{name: "NaN", markups: map[string]float64{"/": math.NaN()}, wantErr: "must be a finite number >= 0"},
		{name: "infinite", markups: map[string]float64{"/": math.Inf(1)}, wantErr: "must be a finite number >= 0"},
		{name: "blank selector", markups: map[string]float64{" ": 5}, wantErr: "selector is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePricingConfig(&PricingConfig{Markups: tt.markups})
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadPricingUserPathMarkupsFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
pricing:
  markups_enabled: true
  markups:
    "/": 20
  user_path_markups:
    /acme:
      "/": 30
      "openai/gpt-5": 15
    /acme/internal:
      "/": 0
`
		result := loadConfigYAML(t, dir, yamlConfig)

		assert.Equal(t, map[string]map[string]float64{
			"/acme":          {"/": 30, "openai/gpt-5": 15},
			"/acme/internal": {"/": 0},
		}, result.Config.Pricing.UserPathMarkups)
	})
}

func TestValidatePricingConfigUserPathMarkups(t *testing.T) {
	tests := []struct {
		name            string
		userPathMarkups map[string]map[string]float64
		wantErr         string
	}{
		{name: "valid", userPathMarkups: map[string]map[string]float64{"/acme": {"/": 0, "openai/": 12.5}}},
		{name: "negative", userPathMarkups: map[string]map[string]float64{"/acme": {"/": -5}}, wantErr: `pricing.user_path_markups["/acme"]["/"] must be a finite number >= 0`},
		{name: "NaN", userPathMarkups: map[string]map[string]float64{"/acme": {"/": math.NaN()}}, wantErr: "must be a finite number >= 0"},
		{name: "blank selector", userPathMarkups: map[string]map[string]float64{"/acme": {" ": 5}}, wantErr: "selector is required"},
		{name: "blank user path", userPathMarkups: map[string]map[string]float64{" ": {"/": 5}}, wantErr: "user path is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePricingConfig(&PricingConfig{UserPathMarkups: tt.userPathMarkups})
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
