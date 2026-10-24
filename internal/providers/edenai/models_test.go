package edenai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// edenCatalogEntry is a verbatim entry from a live Eden /v3/models response.
// Keeping the real shape (including the null members and both pricing blocks)
// is what makes the mapping assertions below meaningful.
const edenCatalogEntry = `{
	"id": "deepinfra/inclusionAI/Ling-3.0-flash-VL",
	"object": "model",
	"created": 1788880306,
	"owned_by": "deepinfra",
	"model_name": "inclusionAI/Ling-3.0-flash-VL",
	"context_length": 131072,
	"description": null,
	"source": null,
	"capabilities": {
		"input_modalities": ["text", "image"],
		"output_modalities": ["text"],
		"supports_reasoning": true,
		"supports_web_search": false,
		"supports_tool_choice": false,
		"supports_computer_use": false,
		"supports_prompt_caching": true,
		"supports_response_schema": false,
		"supports_system_messages": false,
		"supports_function_calling": false,
		"supports_native_streaming": false,
		"supports_assistant_prefill": false,
		"supports_embedding_image_input": false,
		"supports_parallel_function_calling": false
	},
	"pricing": {
		"input_cost_per_token": 6e-8,
		"output_cost_per_token": 1.8e-7,
		"cache_read_input_token_cost": 1.2e-8
	},
	"list_pricing": {
		"input_cost_per_token": 9e-8,
		"output_cost_per_token": 2.8e-7,
		"cache_read_input_token_cost": 2.2e-8
	},
	"discount": null,
	"regions": [{"code": "us", "name": "United States"}],
	"alias_of": null
}`

// modelsServer serves one /models payload and records the request.
func modelsServer(t *testing.T, payload string) (*Provider, *string, *string) {
	t.Helper()
	gotPath := new("")
	gotAuth := new("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath = r.URL.Path
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{}), gotPath, gotAuth
}

func firstModel(t *testing.T, payload string) core.Model {
	t.Helper()
	provider, _, _ := modelsServer(t, payload)
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 1, "models = %+v, want exactly one entry", resp.Data)
	return resp.Data[0]
}

// TestListModels_MapsLiveCatalogEntry asserts the full mapping of a real Eden
// catalog entry: identity, context window, capabilities, modalities, derived
// categories, and per-token pricing scaled to per-million-token.
func TestListModels_MapsLiveCatalogEntry(t *testing.T) {
	provider, gotPath, gotAuth := modelsServer(t, `{"object":"list","data":[`+edenCatalogEntry+`]}`)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/models", *gotPath)
	require.Equal(t, "Bearer edenai-key", *gotAuth)
	require.Equal(t, "list", resp.Object, "response = %+v, want a one-entry list", resp)
	require.Len(t, resp.Data, 1, "response = %+v, want a one-entry list", resp)

	model := resp.Data[0]
	assert.Equal(t, "deepinfra/inclusionAI/Ling-3.0-flash-VL", model.ID, "want the Eden ID forwarded unchanged")
	assert.Equal(t, "model", model.Object, "identity = %+v, want object/owned_by/created preserved", model)
	assert.Equal(t, "deepinfra", model.OwnedBy, "identity = %+v, want object/owned_by/created preserved", model)
	assert.Equal(t, int64(1788880306), model.Created, "identity = %+v, want object/owned_by/created preserved", model)

	meta := model.Metadata
	require.NotNil(t, meta, "Metadata = nil, want Eden catalog metadata")
	require.NotNil(t, meta.ContextWindow, "ContextWindow = nil, want 131072")
	assert.Equal(t, 131072, *meta.ContextWindow)

	// output_modalities ["text"] -> chat + responses (Responses is served by
	// translating through chat completions).
	assert.Equal(t, []string{"chat", "responses"}, meta.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, meta.Categories)

	// Only the true supports_* flags become capabilities, with the prefix
	// stripped; input_modalities ["text","image"] adds vision.
	wantCapabilities := map[string]bool{"reasoning": true, "prompt_caching": true, "vision": true}
	assert.Len(t, meta.Capabilities, len(wantCapabilities), "Capabilities = %v, want %v", meta.Capabilities, wantCapabilities)
	for name := range wantCapabilities {
		assert.True(t, meta.Capabilities[name], "Capabilities[%q] = false, want true", name)
	}
	assert.False(t, meta.Capabilities["web_search"], "Capabilities = %v, want false flags omitted", meta.Capabilities)
	assert.False(t, meta.Capabilities["function_calling"], "Capabilities = %v, want false flags omitted", meta.Capabilities)

	// 6e-8 USD/token -> $0.06/MTok, 1.8e-7 -> $0.18, 1.2e-8 -> $0.012.
	// The values come from `pricing`, not the higher `list_pricing` block.
	assertPrice(t, "InputPerMtok", meta.Pricing.InputPerMtok, 0.06)
	assertPrice(t, "OutputPerMtok", meta.Pricing.OutputPerMtok, 0.18)
	assertPrice(t, "CachedInputPerMtok", meta.Pricing.CachedInputPerMtok, 0.012)
	assert.Equal(t, "USD", meta.Pricing.Currency)
}

// TestListModels_UsesDiscountedPricingNotListPricing pins the choice of block:
// cost metadata must describe what the account is actually charged.
func TestListModels_UsesDiscountedPricingNotListPricing(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[`+edenCatalogEntry+`]}`)
	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	require.NotEqual(t, 0.09, *model.Metadata.Pricing.InputPerMtok, "InputPerMtok took the list_pricing rate; want the discounted pricing block")
}

// TestListModels_FallsBackToListPricing asserts the undiscounted rate card is
// used when Eden publishes no applicable pricing. An approximate rate still
// lets price filters and the cost strategy rank the model; no rate at all
// drops it from both.
func TestListModels_FallsBackToListPricing(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": null,
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
	}]}`)

	require.NotNil(t, model.Metadata, "Pricing = nil, want the list_pricing fallback")
	require.NotNil(t, model.Metadata.Pricing, "Pricing = nil, want the list_pricing fallback")
	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.09)
	assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.28)
}

// TestListModels_ListPricingDoesNotMaskApplicablePricing asserts the fallback
// never overrides a usable account rate: a model priced for input keeps its own
// input rate even though the list card also carries one.
func TestListModels_ListPricingDoesNotMaskApplicablePricing(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"input_cost_per_token": 6e-8, "output_cost_per_token": 1.8e-7},
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
	}]}`)

	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.18)
}

// TestListModels_ListPricingFillsOnlyMissingRates asserts the fallback is per
// rate, not per block: an account block that prices input but not output keeps
// its own input rate and takes only the missing output rate from the list card.
//
// Leaving the gap unpriced is not the safer option it looks like. A nil
// OutputPerMtok makes usage.CalculateGranularCost skip the output side
// entirely, so output tokens are billed at $0 and the recorded total is
// silently short, with no caveat attached. The undiscounted list rate
// overstates the output somewhat, which is the same trade the whole-block
// fallback already accepts (see TestListModels_FallsBackToListPricing), and is
// far closer to the real charge than zero.
func TestListModels_ListPricingFillsOnlyMissingRates(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"input_cost_per_token": 6e-8},
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
	}]}`)

	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.28)
}

// TestListModels_ListPricingFillsMissingInputRate is the mirror case: an
// account block that prices only output keeps that rate and fills input from
// the list card.
func TestListModels_ListPricingFillsMissingInputRate(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"output_cost_per_token": 1.8e-7},
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
	}]}`)

	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.09)
	assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.18)
}

// TestListModels_ListPricingFillsSeveralMissingRates covers an account block
// missing more than one rate, and confirms the rates it does carry -- including
// an explicit zero -- are still preferred field by field.
func TestListModels_ListPricingFillsSeveralMissingRates(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"input_cost_per_token": 0},
		"list_pricing": {
			"input_cost_per_token": 9e-8,
			"output_cost_per_token": 2.8e-7,
			"cache_read_input_token_cost": 1.2e-8,
			"cache_creation_input_token_cost": 1.5e-7
		}
	}]}`)

	pricing := model.Metadata.Pricing
	// An explicit account zero is a real price and must not be filled in.
	assertPrice(t, "InputPerMtok", pricing.InputPerMtok, 0)
	assertPrice(t, "OutputPerMtok", pricing.OutputPerMtok, 0.28)
	assertPrice(t, "CachedInputPerMtok", pricing.CachedInputPerMtok, 0.012)
	assertPrice(t, "CacheWritePerMtok", pricing.CacheWritePerMtok, 0.15)
}

// TestListModels_UnusableAccountRateFallsBackToList asserts a rate the account
// block publishes but perMtok rejects (negative, non-finite, overflowing) is
// treated like an absent one, so the list card can still supply a usable number
// instead of the model losing that rate entirely.
func TestListModels_UnusableAccountRateFallsBackToList(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate string
	}{
		{"negative", "-1e-8"},
		{"overflowing", "1e308"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := firstModel(t, `{"object":"list","data":[{
				"id":"openai/gpt-4","object":"model",
				"pricing": {"input_cost_per_token": `+tc.rate+`, "output_cost_per_token": 1.8e-7},
				"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
			}]}`)

			assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.09)
			assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.18)
		})
	}
}

// TestListModels_UnusableRateInBothBlocksStaysUnpriced asserts a rate no block
// publishes usably is left absent rather than invented.
func TestListModels_UnusableRateInBothBlocksStaysUnpriced(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"input_cost_per_token": 6e-8, "output_cost_per_token": -2e-7},
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": -2.8e-7}
	}]}`)

	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	assert.Nil(t, model.Metadata.Pricing.OutputPerMtok, "OutputPerMtok want nil: neither block published a usable output rate")
}

// TestListModels_CachePricingFields asserts both of Eden's cache rates reach
// the matching core.ModelPricing fields, and that the rates whose usage
// semantics are unconfirmed stay unmapped.
//
// The reasoning and audio rates are the ones deliberately skipped: see
// modelPricing. Asserting they stay nil keeps a future change from wiring them
// up without first establishing how Eden reports the matching token counts.
func TestListModels_CachePricingFields(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {
			"input_cost_per_token": 6e-8,
			"output_cost_per_token": 1.8e-7,
			"cache_read_input_token_cost": 1.2e-8,
			"cache_creation_input_token_cost": 7.5e-8,
			"output_cost_per_reasoning_token": 3.6e-7,
			"input_cost_per_audio_token": 1e-6
		}
	}]}`)

	pricing := model.Metadata.Pricing
	assertPrice(t, "CachedInputPerMtok", pricing.CachedInputPerMtok, 0.012)
	assertPrice(t, "CacheWritePerMtok", pricing.CacheWritePerMtok, 0.075)
	assert.Nil(t, pricing.ReasoningOutputPerMtok, "ReasoningOutputPerMtok want nil: Eden reports no reasoning token count to price against")
	assert.Nil(t, pricing.AudioInputPerMtok, "AudioInputPerMtok want nil: Eden reports no audio token count to price against")
}

// TestListModels_TieredAndPerQueryPricingIgnored asserts the Eden pricing
// members the gateway has no equivalent for are skipped rather than guessed at.
// Eden publishes context-length-tiered rates, a tiered_pricing list, and
// per-query search fees (an object, not a scalar); reading any of them into a
// flat per-Mtok field would misprice the model.
func TestListModels_TieredAndPerQueryPricingIgnored(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {
			"input_cost_per_token": 6e-8,
			"output_cost_per_token": 1.8e-7,
			"input_cost_per_token_above_200k_tokens": 1.2e-7,
			"output_cost_per_token_above_200k_tokens": 3.6e-7,
			"search_context_cost_per_query": {"search_context_size_low": 0.01},
			"tiered_pricing": [{"input_cost_per_token": 5e-8}]
		}
	}]}`)

	pricing := model.Metadata.Pricing
	assertPrice(t, "InputPerMtok", pricing.InputPerMtok, 0.06)
	assertPrice(t, "OutputPerMtok", pricing.OutputPerMtok, 0.18)
	assert.Empty(t, pricing.Tiers, "Tiers = %v, want empty: Eden's tiered_pricing shape is not mapped", pricing.Tiers)
	assert.Nil(t, pricing.PerRequest, "PerRequest want nil: per-query search fees are not per-request charges")
}

// TestListModels_PricingEdgeCases covers partial, zero, negative, and
// overflowing rates. A rate Eden omits must stay absent rather than being
// costed at zero; a rate Eden reports as zero must be honoured as free.
func TestListModels_PricingEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		pricing    string
		wantNil    bool
		wantInput  *float64
		wantOutput *float64
		wantCached *float64
	}{
		{
			name:    "no pricing member",
			pricing: `null`,
			wantNil: true,
		},
		{
			name:    "empty pricing object",
			pricing: `{}`,
			wantNil: true,
		},
		{
			name:      "partial pricing keeps the reported rate and omits the rest",
			pricing:   `{"input_cost_per_token": 6e-8}`,
			wantInput: new(0.06),
		},
		{
			name:       "explicit zero prices at zero",
			pricing:    `{"input_cost_per_token": 0, "output_cost_per_token": 0}`,
			wantInput:  new(0.0),
			wantOutput: new(0.0),
		},
		{
			name:       "negative rates are rejected, valid siblings survive",
			pricing:    `{"input_cost_per_token": -1e-8, "output_cost_per_token": 1.8e-7}`,
			wantOutput: new(0.18),
		},
		{
			name:    "every rate invalid yields no pricing",
			pricing: `{"input_cost_per_token": -1e-8, "output_cost_per_token": -2e-8}`,
			wantNil: true,
		},
		{
			name:       "a rate that overflows on scaling is dropped",
			pricing:    `{"input_cost_per_token": 1e308, "output_cost_per_token": 1.8e-7}`,
			wantOutput: new(0.18),
		},
		{
			name:       "cached input rate alone is still pricing",
			pricing:    `{"cache_read_input_token_cost": 1.2e-8}`,
			wantCached: new(0.012),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := `{"object":"list","data":[{"id":"openai/gpt-4","object":"model","pricing":` + tt.pricing + `}]}`
			model := firstModel(t, payload)

			if tt.wantNil {
				if model.Metadata != nil {
					require.Nil(t, model.Metadata.Pricing, "Pricing = %+v, want nil", model.Metadata.Pricing)
				}
				return
			}
			require.NotNil(t, model.Metadata, "Pricing = nil, want a partial pricing block")
			require.NotNil(t, model.Metadata.Pricing, "Pricing = nil, want a partial pricing block")
			assertOptionalPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, tt.wantInput)
			assertOptionalPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, tt.wantOutput)
			assertOptionalPrice(t, "CachedInputPerMtok", model.Metadata.Pricing.CachedInputPerMtok, tt.wantCached)
		})
	}
}

// TestPerMtok_RejectsNonFiniteRates covers NaN and infinity directly: neither
// can be expressed as a JSON number, so they can only arrive through a decoder
// that tolerates them, and both must be refused rather than scaled.
func TestPerMtok_RejectsNonFiniteRates(t *testing.T) {
	for _, rate := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		_, ok := perMtok(&rate)
		assert.False(t, ok, "perMtok(%v) reported a usable price, want rejected", rate)
	}
	_, ok := perMtok(nil)
	assert.False(t, ok, "perMtok(nil) reported a usable price, want rejected")
	value, ok := perMtok(new(6e-8))
	assert.True(t, ok, "perMtok(6e-8) = %v, %v; want 0.06, true", value, ok)
	assert.InDelta(t, 0.06, value, 1e-9, "perMtok(6e-8) = %v, %v; want 0.06, true", value, ok)
}

// TestListModels_ModalityMapping asserts output modalities become modes (so
// the registry can hide models this provider cannot serve) and input
// modalities become capabilities.
func TestListModels_ModalityMapping(t *testing.T) {
	tests := []struct {
		name             string
		capabilities     string
		wantModes        []string
		wantCapabilities []string
	}{
		{
			name:         "text only",
			capabilities: `{"output_modalities":["text"]}`,
			wantModes:    []string{"chat", "responses"},
		},
		{
			name:         "image output becomes image_generation",
			capabilities: `{"output_modalities":["image"]}`,
			wantModes:    []string{"image_generation"},
		},
		{
			name:         "audio output becomes audio_speech",
			capabilities: `{"output_modalities":["audio"]}`,
			wantModes:    []string{"audio_speech"},
		},
		{
			name:         "embedding output",
			capabilities: `{"output_modalities":["embeddings"]}`,
			wantModes:    []string{"embedding"},
		},
		{
			name:             "multimodal input adds capabilities",
			capabilities:     `{"output_modalities":["text"],"input_modalities":["text","image","audio","video"]}`,
			wantModes:        []string{"chat", "responses"},
			wantCapabilities: []string{"vision", "audio", "video"},
		},
		{
			// Eden's live catalog never publishes a video output modality
			// (video appears only as an input), but the mode is mapped so a
			// model Eden adds later is classified rather than silently
			// advertised as chat.
			name:         "video output becomes video_generation",
			capabilities: `{"output_modalities":["video"]}`,
			wantModes:    []string{"video_generation"},
		},
		{
			// A model that outputs both text and video keeps its chat modes,
			// so it stays advertised and is reached through chat.
			name:         "video alongside text keeps the chat modes",
			capabilities: `{"output_modalities":["text","video"]}`,
			wantModes:    []string{"chat", "responses", "video_generation"},
		},
		{
			// Text maps to two modes, so a repeated modality would duplicate
			// them without the dedup in modes().
			name:         "repeated modality is deduplicated",
			capabilities: `{"output_modalities":["text","text","image","image"]}`,
			wantModes:    []string{"chat", "responses", "image_generation"},
		},
		{
			name:         "unknown modality is ignored",
			capabilities: `{"output_modalities":["telepathy"]}`,
			wantModes:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := `{"object":"list","data":[{"id":"m","object":"model","capabilities":` + tt.capabilities + `}]}`
			model := firstModel(t, payload)

			var modes []string
			if model.Metadata != nil {
				modes = model.Metadata.Modes
			}
			assert.Equal(t, tt.wantModes, modes)
			for _, capability := range tt.wantCapabilities {
				require.NotNil(t, model.Metadata, "Capabilities missing %q", capability)
				assert.True(t, model.Metadata.Capabilities[capability], "Capabilities missing %q", capability)
			}
		})
	}
}

// TestListModels_SkipsInvalidEntriesAndKeepsBareOnes asserts a blank ID is
// dropped and an entry with nothing to enrich keeps Metadata nil rather than
// an empty struct that enrichment would read as a real provider report.
func TestListModels_SkipsInvalidEntriesAndKeepsBareOnes(t *testing.T) {
	provider, _, _ := modelsServer(t, `{"object":"list","data":[
		{"id":"   ","object":"model"},
		{"id":"openai/gpt-4","object":"model"},
		{"id":"openai/gpt-5","object":"","context_length":0}
	]}`)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 2, "models = %+v, want the blank ID dropped", resp.Data)
	assert.Equal(t, "openai/gpt-4", resp.Data[0].ID, "bare entry = %+v", resp.Data[0])
	assert.Nil(t, resp.Data[0].Metadata, "bare entry = %+v, want nil Metadata", resp.Data[0])
	assert.Equal(t, "model", resp.Data[1].Object, `want the default "model" applied`)
}

// TestListModels_PropagatesUpstreamError asserts a failed catalog fetch
// surfaces as an error, so the registry records the failure and keeps the
// provider registered for the next refresh instead of publishing an empty
// catalog as though Eden had no models.
func TestListModels_PropagatesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	require.Error(t, err, "ListModels() error = nil, want the upstream failure; resp = %+v", resp)
	assert.Nil(t, resp, "ListModels() resp = %+v, want nil on error", resp)
}

func assertPrice(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	require.NotNil(t, got, "%s = nil, want %v", name, want)
	assert.InDelta(t, want, *got, 1e-9, "%s = %v, want %v", name, *got, want)
}

func assertOptionalPrice(t *testing.T, name string, got, want *float64) {
	t.Helper()
	if want == nil {
		assert.Nil(t, got, "%s want nil (an unreported rate must not be costed)", name)
		return
	}
	assertPrice(t, name, got, *want)
}
