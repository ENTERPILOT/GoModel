package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_NilList(t *testing.T) {
	meta := Resolve(nil, "openai", "gpt-4o")
	assert.Nil(t, meta)
}

func TestResolve_NoMatch(t *testing.T) {
	list := &ModelList{
		Models:         map[string]ModelEntry{},
		ProviderModels: map[string]ProviderModelEntry{},
	}
	meta := Resolve(list, "openai", "nonexistent-model")
	assert.Nil(t, meta)
}

func TestResolve_DirectModelMatch(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:     "GPT-4o",
				Description:     new("Flagship model"),
				Family:          new("gpt-4o"),
				Modes:           []string{"chat"},
				Tags:            []string{"flagship", "multimodal"},
				ContextWindow:   new(128000),
				MaxOutputTokens: new(16384),
				Capabilities: map[string]bool{
					"function_calling": true,
					"streaming":        true,
					"vision":           true,
				},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "gpt-4o")
	require.NotNil(t, meta, "expected non-nil metadata")

	assert.Equal(t, "GPT-4o", meta.DisplayName)
	assert.Equal(t, "Flagship model", meta.Description)
	assert.Equal(t, "gpt-4o", meta.Family)
	require.Len(t, meta.Modes, 1)
	assert.Equal(t, "chat", meta.Modes[0])
	assert.Len(t, meta.Tags, 2)
	require.NotNil(t, meta.ContextWindow)
	assert.Equal(t, 128000, *meta.ContextWindow)
	require.NotNil(t, meta.MaxOutputTokens)
	assert.Equal(t, 16384, *meta.MaxOutputTokens)
	assert.True(t, meta.Capabilities["function_calling"])

	require.NotNil(t, meta.Pricing, "expected non-nil pricing")
	assert.Equal(t, "USD", meta.Pricing.Currency)
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 2.50, *meta.Pricing.InputPerMtok)
	require.NotNil(t, meta.Pricing.OutputPerMtok)
	assert.Equal(t, 10.00, *meta.Pricing.OutputPerMtok)
	got := meta.PricingSources["input_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceModelRegistry, got)
	got = meta.PricingSources["output_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceModelRegistry, got)
}

func TestResolve_ProviderModelOverride(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:     "GPT-4o",
				Modes:           []string{"chat"},
				ContextWindow:   new(128000),
				MaxOutputTokens: new(16384),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"azure/gpt-4o": {
				ModelRef:      "gpt-4o",
				Enabled:       true,
				ContextWindow: new(64000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(5.00),
					OutputPerMtok: new(15.00),
				},
			},
		},
	}

	meta := Resolve(list, "azure", "gpt-4o")
	require.NotNil(t, meta, "expected non-nil metadata")

	// Provider model should override context_window
	require.NotNil(t, meta.ContextWindow)
	assert.Equal(t, 64000, *meta.ContextWindow)

	// max_output_tokens should come from base model (not overridden)
	require.NotNil(t, meta.MaxOutputTokens)
	assert.Equal(t, 16384, *meta.MaxOutputTokens)

	// Pricing should be overridden
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 5.00, *meta.Pricing.InputPerMtok)
	require.NotNil(t, meta.Pricing.OutputPerMtok)
	assert.Equal(t, 15.00, *meta.Pricing.OutputPerMtok)
	got := meta.PricingSources["input_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceModelRegistry, got)
	got = meta.PricingSources["output_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceModelRegistry, got)

	// DisplayName from base model
	assert.Equal(t, "GPT-4o", meta.DisplayName)
}

func TestResolve_MapsRankingsIntoMetadata(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
				Rankings: map[string]RankingEntry{
					"chatbot_arena": {
						Elo:  new(1287.0),
						Rank: new(3),
						AsOf: new("2026-02-01"),
					},
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "gpt-4o")
	require.NotNil(t, meta, "expected non-nil metadata")
	ranking, ok := meta.Rankings["chatbot_arena"]
	require.True(t, ok)
	require.NotNil(t, ranking.Elo)
	require.Equal(t, 1287.0, *ranking.Elo)
	require.NotNil(t, ranking.Rank)
	require.Equal(t, 3, *ranking.Rank)
	require.Equal(t, "2026-02-01", ranking.AsOf)
}

func TestResolve_ProviderModelWithoutBaseModel(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{},
		ProviderModels: map[string]ProviderModelEntry{
			"custom/my-model": {
				ModelRef:      "nonexistent",
				Enabled:       true,
				ContextWindow: new(32000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(1.00),
					OutputPerMtok: new(2.00),
				},
			},
		},
	}

	meta := Resolve(list, "custom", "my-model")
	require.NotNil(t, meta, "expected non-nil metadata even without base model")

	require.NotNil(t, meta.ContextWindow)
	assert.Equal(t, 32000, *meta.ContextWindow)
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 1.00, *meta.Pricing.InputPerMtok)
}

func TestResolve_NilPricing(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"text-moderation": {
				DisplayName: "Text Moderation",
				Modes:       []string{"moderation"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "text-moderation")
	require.NotNil(t, meta, "expected non-nil metadata")
	assert.Nil(t, meta.Pricing)
}

func TestResolve_SetsCategoriesFromModes(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
			},
			"dall-e-3": {
				DisplayName: "DALL-E 3",
				Modes:       []string{"image_generation"},
			},
			"whisper-1": {
				DisplayName: "Whisper",
				Modes:       []string{"audio_transcription"},
			},
			"text-moderation": {
				DisplayName: "Moderation",
				Modes:       []string{"moderation"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	tests := []struct {
		modelID  string
		wantCats []core.ModelCategory
	}{
		{"gpt-4o", []core.ModelCategory{core.CategoryTextGeneration}},
		{"dall-e-3", []core.ModelCategory{core.CategoryImage}},
		{"whisper-1", []core.ModelCategory{core.CategoryAudio}},
		{"text-moderation", []core.ModelCategory{core.CategoryUtility}},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			meta := Resolve(list, "openai", tt.modelID)
			require.NotNil(t, meta, "expected non-nil metadata")
			require.Len(t, meta.Categories, len(tt.wantCats), "Categories = %v, want %v", meta.Categories, tt.wantCats)

			for i, c := range meta.Categories {
				assert.Equal(t, tt.wantCats[i], c, "Categories[%d] = %q, want %q", i, c, tt.wantCats[i])
			}
		})
	}
}

// Verify Resolve handles the three-layer merge correctly:
// base model fields + provider_model overrides
func TestResolve_ThreeLayerMerge(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"claude-sonnet-4-20250514": {
				DisplayName:     "Claude Sonnet 4",
				Description:     new("Fast, intelligent model"),
				Family:          new("claude-sonnet"),
				Modes:           []string{"chat"},
				Tags:            []string{"flagship"},
				ContextWindow:   new(200000),
				MaxOutputTokens: new(16384),
				Capabilities: map[string]bool{
					"function_calling": true,
					"vision":           true,
				},
				Pricing: &core.ModelPricing{
					Currency:           "USD",
					InputPerMtok:       new(3.00),
					OutputPerMtok:      new(15.00),
					CachedInputPerMtok: new(0.30),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"bedrock/claude-sonnet-4-20250514": {
				ModelRef:        "claude-sonnet-4-20250514",
				Enabled:         true,
				MaxOutputTokens: new(8192),
				Pricing: &core.ModelPricing{
					Currency:           "USD",
					InputPerMtok:       new(3.00),
					OutputPerMtok:      new(15.00),
					CachedInputPerMtok: new(0.30),
				},
			},
		},
	}

	// Direct provider (anthropic) - should use base model only
	meta := Resolve(list, "anthropic", "claude-sonnet-4-20250514")
	require.NotNil(t, meta, "expected non-nil metadata")
	require.NotNil(t, meta.MaxOutputTokens)
	assert.Equal(t, 16384, *meta.MaxOutputTokens)

	// Bedrock - should override max_output_tokens
	meta = Resolve(list, "bedrock", "claude-sonnet-4-20250514")
	require.NotNil(t, meta, "expected non-nil metadata for bedrock")
	require.NotNil(t, meta.MaxOutputTokens)
	assert.Equal(t, 8192, *meta.MaxOutputTokens)

	// DisplayName should still come from base
	assert.Equal(t, "Claude Sonnet 4", meta.DisplayName)
}

func TestResolve_ReverseCustomModelIDLookup(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:   "GPT-4o",
				Modes:         []string{"chat"},
				ContextWindow: new(128000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openai/gpt-4o": {
				ModelRef:      "gpt-4o",
				CustomModelID: new("gpt-4o-2024-08-06"),
				Enabled:       true,
			},
		},
	}
	list.buildReverseIndex()

	// Resolve using the dated response model ID
	meta := Resolve(list, "openai", "gpt-4o-2024-08-06")
	require.NotNil(t, meta, "expected non-nil metadata via reverse lookup")
	assert.Equal(t, "GPT-4o", meta.DisplayName)

	require.NotNil(t, meta.Pricing, "expected non-nil pricing via reverse lookup")
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 2.50, *meta.Pricing.InputPerMtok)
}

func TestResolve_ReverseIndexNotBuilt(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openai/gpt-4o": {
				ModelRef:      "gpt-4o",
				CustomModelID: new("gpt-4o-blue"),
				Enabled:       true,
			},
		},
		// providerModelByActualID is nil (buildReverseIndex not called)
	}

	meta := Resolve(list, "openai", "gpt-4o-blue")
	assert.Nil(t, meta)
}

func TestResolve_ReleaseDateSuffixFallback(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"glm-5.1": {
				DisplayName: "GLM 5.1",
				Modes:       []string{"chat"},
				Aliases: []string{
					"z-ai/glm-5.1",
					"openrouter/z-ai/glm-5.1",
				},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(1.05),
					OutputPerMtok: new(3.50),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openrouter/glm-5.1": {
				ModelRef: "glm-5.1",
				Enabled:  true,
				Pricing: &core.ModelPricing{
					Currency:           "USD",
					InputPerMtok:       new(1.05),
					OutputPerMtok:      new(3.50),
					CachedInputPerMtok: new(0.525),
				},
			},
		},
	}
	list.buildReverseIndex()

	for _, modelID := range []string{
		"z-ai/glm-5.1-20260406",
		"z-ai/glm-5.1-2026-04-06",
		"z-ai/glm-5.1-2026",
	} {
		t.Run(modelID, func(t *testing.T) {
			meta := Resolve(list, "openrouter", modelID)
			require.NotNil(t, meta)
			require.NotNil(t, meta.Pricing)
			require.NotNil(t, meta.Pricing.CachedInputPerMtok)
			require.NotNil(t, meta.Pricing.InputPerMtok)
			assert.Equal(t, 1.05, *meta.Pricing.InputPerMtok)
			assert.Equal(t, 0.525, *meta.Pricing.CachedInputPerMtok)
		})
	}
}

func TestResolve_ReleaseDateSuffixFallbackExactMatchWins(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"glm-5.1": {
				DisplayName: "GLM 5.1",
				Modes:       []string{"chat"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openrouter/z-ai/glm-5.1-20260406": {
				ModelRef: "glm-5.1",
				Enabled:  true,
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.00),
					OutputPerMtok: new(4.00),
				},
			},
			"openrouter/glm-5.1": {
				ModelRef: "glm-5.1",
				Enabled:  true,
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(1.05),
					OutputPerMtok: new(3.50),
				},
			},
		},
	}

	meta := Resolve(list, "openrouter", "z-ai/glm-5.1-20260406")
	require.NotNil(t, meta)
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 2.00, *meta.Pricing.InputPerMtok)
}

func TestResolve_ReverseIndexWithProviderModelOverride(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:   "GPT-4o",
				Modes:         []string{"chat"},
				ContextWindow: new(128000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openai/gpt-4o": {
				ModelRef:      "gpt-4o",
				CustomModelID: new("gpt-4o-2024-08-06"),
				Enabled:       true,
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(3.00),
					OutputPerMtok: new(12.00),
				},
			},
		},
	}
	list.buildReverseIndex()

	// Reverse lookup should resolve and apply provider_model pricing override
	meta := Resolve(list, "openai", "gpt-4o-2024-08-06")
	require.NotNil(t, meta, "expected non-nil metadata via reverse lookup")
	require.NotNil(t, meta.Pricing, "expected non-nil pricing")
	// Should use the provider_model override, not the base model pricing
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 3.00, *meta.Pricing.InputPerMtok)
	require.NotNil(t, meta.Pricing.OutputPerMtok)
	assert.Equal(t, 12.00, *meta.Pricing.OutputPerMtok)
}

func TestResolve_ModelAliasUsesProviderOverride(t *testing.T) {
	list := &ModelList{
		Providers: map[string]ProviderEntry{
			"gemini": {DisplayName: "Gemini"},
		},
		Models: map[string]ModelEntry{
			"claude-4-opus": {
				DisplayName: "Claude 4 Opus",
				Modes:       []string{"chat"},
				Aliases:     []string{"claude-opus-4", "gemini/claude-opus-4"},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(15.00),
					OutputPerMtok: new(75.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"gemini/claude-4-opus": {
				ModelRef:      "claude-4-opus",
				Enabled:       true,
				ContextWindow: new(200000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(12.00),
					OutputPerMtok: new(60.00),
				},
			},
		},
	}
	list.buildReverseIndex()

	meta := Resolve(list, "gemini", "claude-opus-4")
	require.NotNil(t, meta, "expected non-nil metadata via model alias")
	require.Equal(t, "Claude 4 Opus", meta.DisplayName)
	require.NotNil(t, meta.ContextWindow)
	require.Equal(t, 200000, *meta.ContextWindow)
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	require.Equal(t, 12.00, *meta.Pricing.InputPerMtok)
}

func TestResolve_AmbiguousModelAliasReturnsNil(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"model-a": {
				DisplayName: "Model A",
				Modes:       []string{"chat"},
				Aliases:     []string{"shared-alias"},
			},
			"model-b": {
				DisplayName: "Model B",
				Modes:       []string{"chat"},
				Aliases:     []string{"shared-alias"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}
	list.buildReverseIndex()

	meta := Resolve(list, "openai", "shared-alias")
	require.Nil(t, meta)
}

func TestResolve_RoutingSuffixFallback(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-5.6-luna-pro": {
				DisplayName:   "GPT-5.6 Luna Pro",
				Modes:         []string{"chat"},
				ContextWindow: new(1050000),
				Aliases: []string{
					"openai/gpt-5.6-luna-pro",
					"openrouter/openai/gpt-5.6-luna-pro",
				},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(0.20),
					OutputPerMtok: new(1.20),
				},
			},
		},
	}
	list.buildReverseIndex()

	for _, modelID := range []string{
		"openrouter/openai/gpt-5.6-luna-pro:floor",
		"openrouter/openai/gpt-5.6-luna-pro:nitro",
		"openai/gpt-5.6-luna-pro:free",
	} {
		t.Run(modelID, func(t *testing.T) {
			meta := Resolve(list, "openrouter", modelID)
			require.NotNil(t, meta)
			assert.Equal(t, "GPT-5.6 Luna Pro", meta.DisplayName)
			require.NotNil(t, meta.ContextWindow)
			assert.Equal(t, 1050000, *meta.ContextWindow)
		})
	}
}

func TestResolve_RoutingSuffixDoesNotShadowExplicitEntry(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"deepseek-r1": {
				DisplayName:   "DeepSeek R1",
				ContextWindow: new(128000),
				Aliases:       []string{"deepseek/deepseek-r1"},
			},
			"deepseek-r1-free": {
				DisplayName:   "DeepSeek R1 (free)",
				ContextWindow: new(64000),
				Aliases:       []string{"deepseek/deepseek-r1:free"},
			},
		},
	}
	list.buildReverseIndex()

	meta := Resolve(list, "openrouter", "deepseek/deepseek-r1:free")
	require.NotNil(t, meta)

	// The suffixed ID has an entry of its own, so the fallback must not run.
	assert.Equal(t, "DeepSeek R1 (free)", meta.DisplayName)
}

func TestResolve_UnknownSuffixDoesNotBorrowMetadata(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"anthropic.claude-3-5-haiku-20241022-v1": {
				DisplayName:   "Claude 3.5 Haiku",
				ContextWindow: new(200000),
				Aliases:       []string{"bedrock/anthropic.claude-3-5-haiku-20241022-v1"},
			},
		},
	}
	list.buildReverseIndex()
	meta := // Bedrock encodes a model version after the colon; ":0" is not a routing
		// variant, so the fallback must not resolve it to the unsuffixed entry.
		Resolve(list, "bedrock", "anthropic.claude-3-5-haiku-20241022-v1:0")
	require.Nil(t, meta)
}

func TestResolve_RoutingSuffixComposesWithReleaseDate(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"glm-5.1": {
				DisplayName:   "GLM-5.1",
				ContextWindow: new(131072),
				Aliases:       []string{"z-ai/glm-5.1"},
			},
		},
	}
	list.buildReverseIndex()

	// The ID carries both a release date and a routing variant; each layer
	// strips its own and the base entry is still found.
	meta := Resolve(list, "openrouter", "z-ai/glm-5.1-20260406:free")
	require.NotNil(t, meta)
	assert.Equal(t, "GLM-5.1", meta.DisplayName)
}

func TestStripRoutingSuffix(t *testing.T) {
	cases := []struct {
		modelID string
		base    string
		ok      bool
	}{
		{"openrouter/openai/gpt-5.6-luna-pro:floor", "openrouter/openai/gpt-5.6-luna-pro", true},
		{"qwen/qwen3.7-flash:free", "qwen/qwen3.7-flash", true},
		{"gpt-5.6-luna", "", false},
		{"qwen/qwen3.7-flash:", "", false},
		{":floor", "", false},
		// A colon before a slash belongs to the path, not to a variant.
		{"host:8080/model", "", false},
		// Only the known routing variants are stripped: a provider version
		// such as Bedrock's ":0" or an unlisted variant stays untouched.
		{"anthropic.claude-3-5-haiku-20241022-v1:0", "", false},
		{"deepseek/deepseek-r1:thinking", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.modelID, func(t *testing.T) {
			base, ok := stripRoutingSuffix(tc.modelID)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.base, base)
		})
	}
}
