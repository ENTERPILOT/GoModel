package markups

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/usage"
)

type providerCatalog []string

func (c providerCatalog) ProviderNames() []string { return c }

var testCatalog = providerCatalog{"openai", "openai-eu", "anthropic"}

func TestApplySelectsMostSpecificMarkup(t *testing.T) {
	markups := map[string]float64{
		"/":             10,
		"openai/":       20,
		"gpt-5":         30,
		"openai/gpt-5":  40,
		"anthropic/":    0,
		"vendor/model":  50,
		"openai-eu/o3/": 60,
	}
	svc, err := New(config.PricingConfig{Markups: markups, BillCacheHits: true}, testCatalog)
	require.NoError(t, err)

	tests := []struct {
		name         string
		providerName string
		provider     string
		model        string
		wantPercent  float64
		wantSelector string
	}{
		{name: "provider/model beats model and provider", providerName: "openai", provider: "openai", model: "gpt-5", wantPercent: 40, wantSelector: "openai/gpt-5"},
		{name: "model beats provider", providerName: "openai-eu", provider: "openai", model: "gpt-5", wantPercent: 30, wantSelector: "gpt-5"},
		{name: "provider beats global", providerName: "openai", provider: "openai", model: "gpt-4o", wantPercent: 20, wantSelector: "openai/"},
		{name: "zero markup provider still matches", providerName: "anthropic", provider: "anthropic", model: "claude-opus-4-1", wantPercent: 0, wantSelector: "anthropic/"},
		{name: "global fallback", providerName: "groq", provider: "groq", model: "llama-4", wantPercent: 10, wantSelector: "/"},
		{name: "provider type when instance name is missing", provider: "openai", model: "gpt-4o", wantPercent: 20, wantSelector: "openai/"},
		{name: "model prefixed with its provider", providerName: "openai", provider: "openai", model: "openai/gpt-5", wantPercent: 40, wantSelector: "openai/gpt-5"},
		{name: "slash model id on unconfigured provider prefix", providerName: "groq", provider: "groq", model: "vendor/model", wantPercent: 50, wantSelector: "vendor/model"},
		{name: "model id with trailing slash under a provider", providerName: "openai-eu", provider: "openai", model: "o3/", wantPercent: 60, wantSelector: "openai-eu/o3/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &usage.UsageEntry{
				ProviderName: tt.providerName,
				Provider:     tt.provider,
				Model:        tt.model,
				TotalCost:    new(2.0),
			}
			svc.Apply(entry)

			require.NotNil(t, entry.MarkupPercent)
			assert.Equal(t, tt.wantPercent, *entry.MarkupPercent)
			assert.Equal(t, tt.wantSelector, entry.MarkupSelector)
			require.NotNil(t, entry.BilledCost)
			assert.InDelta(t, 2*(1+tt.wantPercent/100), *entry.BilledCost, 1e-12)
			assert.Equal(t, 2.0, *entry.TotalCost, "provider cost must not change")
		})
	}
}

func TestApplyBilledCost(t *testing.T) {
	tests := []struct {
		name          string
		markups       map[string]float64
		billCacheHits bool
		cacheType     string
		totalCost     *float64
		wantPercent   *float64
		wantBilled    *float64
	}{
		{name: "no rules bills provider cost", totalCost: new(1.5), wantBilled: new(1.5)},
		{name: "markup on top of cost", markups: map[string]float64{"/": 20}, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(1.2)},
		{name: "unknown cost stays unknown", markups: map[string]float64{"/": 20}, wantPercent: new(20.0)},
		{name: "free request bills zero", markups: map[string]float64{"/": 20}, totalCost: new(0.0), wantPercent: new(20.0), wantBilled: new(0.0)},
		{name: "exact cache hit billed with markup", markups: map[string]float64{"/": 20}, billCacheHits: true, cacheType: usage.CacheTypeExact, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(1.2)},
		{name: "semantic cache hit billed with markup", markups: map[string]float64{"/": 20}, billCacheHits: true, cacheType: usage.CacheTypeSemantic, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(1.2)},
		{name: "cache hit billed at cost without rules", billCacheHits: true, cacheType: usage.CacheTypeExact, totalCost: new(1.0), wantBilled: new(1.0)},
		{name: "exact cache hit unbilled", markups: map[string]float64{"/": 20}, cacheType: usage.CacheTypeExact, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(0.0)},
		{name: "semantic cache hit unbilled", markups: map[string]float64{"/": 20}, cacheType: usage.CacheTypeSemantic, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(0.0)},
		{name: "switch never affects provider calls", markups: map[string]float64{"/": 20}, totalCost: new(1.0), wantPercent: new(20.0), wantBilled: new(1.2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := New(config.PricingConfig{Markups: tt.markups, BillCacheHits: tt.billCacheHits}, testCatalog)
			require.NoError(t, err)
			entry := &usage.UsageEntry{Provider: "openai", Model: "gpt-5", CacheType: tt.cacheType, TotalCost: tt.totalCost}

			svc.Apply(entry)

			if tt.wantPercent == nil {
				assert.Nil(t, entry.MarkupPercent)
				assert.Empty(t, entry.MarkupSelector)
			} else {
				require.NotNil(t, entry.MarkupPercent)
				assert.Equal(t, *tt.wantPercent, *entry.MarkupPercent)
			}
			if tt.wantBilled == nil {
				assert.Nil(t, entry.BilledCost)
				return
			}
			require.NotNil(t, entry.BilledCost)
			assert.InDelta(t, *tt.wantBilled, *entry.BilledCost, 1e-12)
		})
	}
}

func TestApplyPrefersRoutedModel(t *testing.T) {
	svc, err := New(config.PricingConfig{Markups: map[string]float64{
		"/":                        5,
		"openai/":                  10,
		"openai/gpt-4o-mini":       20,
		"gpt-4o-mini-2024-07-18":   30,
		"openai/gpt-4o-2024-08-06": 40,
	}}, testCatalog)
	require.NoError(t, err)

	tests := []struct {
		name         string
		routed       string
		answered     string
		wantSelector string
	}{
		{name: "routed model rule beats answered model rule", routed: "gpt-4o-mini", answered: "gpt-4o-mini-2024-07-18", wantSelector: "openai/gpt-4o-mini"},
		{name: "answered model rule when routed has none", routed: "gpt-4o", answered: "gpt-4o-2024-08-06", wantSelector: "openai/gpt-4o-2024-08-06"},
		{name: "provider rule when neither model has one", routed: "o3", answered: "o3-2025-04-16", wantSelector: "openai/"},
		{name: "answered model alone", answered: "gpt-4o-mini-2024-07-18", wantSelector: "gpt-4o-mini-2024-07-18"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &usage.UsageEntry{ProviderName: "openai", Provider: "openai", RoutedModel: tt.routed, Model: tt.answered, TotalCost: new(1.0)}
			svc.Apply(entry)
			assert.Equal(t, tt.wantSelector, entry.MarkupSelector)
		})
	}
}

func TestApplyResetsMarkupWhenNoRuleMatches(t *testing.T) {
	svc, err := New(config.PricingConfig{Markups: map[string]float64{"anthropic/": 20}}, testCatalog)
	require.NoError(t, err)
	entry := &usage.UsageEntry{Provider: "openai", Model: "gpt-5", TotalCost: new(1.0), MarkupPercent: new(99.0), MarkupSelector: "stale"}

	svc.Apply(entry)

	assert.Nil(t, entry.MarkupPercent)
	assert.Empty(t, entry.MarkupSelector)
	require.NotNil(t, entry.BilledCost)
	assert.Equal(t, 1.0, *entry.BilledCost)
}

func TestNewRejectsInvalidSelectors(t *testing.T) {
	tests := []struct {
		name    string
		markups map[string]float64
		wantErr string
	}{
		{name: "unknown provider", markups: map[string]float64{"opnai/": 10}, wantErr: "unknown provider name"},
		{name: "blank selector", markups: map[string]float64{"  ": 10}, wantErr: "selector is required"},
		{name: "duplicate after normalization", markups: map[string]float64{"openai/gpt-5": 10, " openai/gpt-5 ": 20}, wantErr: "duplicates selector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(config.PricingConfig{Markups: tt.markups}, testCatalog)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
