package edenai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
	return NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{}), gotPath, gotAuth
}

func firstModel(t *testing.T, payload string) core.Model {
	t.Helper()
	provider, _, _ := modelsServer(t, payload)
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models = %+v, want exactly one entry", resp.Data)
	}
	return resp.Data[0]
}

// TestListModels_MapsLiveCatalogEntry asserts the full mapping of a real Eden
// catalog entry: identity, context window, capabilities, modalities, derived
// categories, and per-token pricing scaled to per-million-token.
func TestListModels_MapsLiveCatalogEntry(t *testing.T) {
	provider, gotPath, gotAuth := modelsServer(t, `{"object":"list","data":[`+edenCatalogEntry+`]}`)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if *gotPath != "/models" {
		t.Fatalf("path = %q, want /models", *gotPath)
	}
	if *gotAuth != "Bearer edenai-key" {
		t.Fatalf("authorization = %q, want Bearer edenai-key", *gotAuth)
	}
	if resp.Object != "list" || len(resp.Data) != 1 {
		t.Fatalf("response = %+v, want a one-entry list", resp)
	}

	model := resp.Data[0]
	if model.ID != "deepinfra/inclusionAI/Ling-3.0-flash-VL" {
		t.Errorf("ID = %q, want the Eden ID forwarded unchanged", model.ID)
	}
	if model.Object != "model" || model.OwnedBy != "deepinfra" || model.Created != 1788880306 {
		t.Errorf("identity = %+v, want object/owned_by/created preserved", model)
	}

	meta := model.Metadata
	if meta == nil {
		t.Fatal("Metadata = nil, want Eden catalog metadata")
	}
	if meta.ContextWindow == nil || *meta.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, want 131072", meta.ContextWindow)
	}

	// output_modalities ["text"] -> chat + responses (Responses is served by
	// translating through chat completions).
	if want := []string{"chat", "responses"}; !slices.Equal(meta.Modes, want) {
		t.Errorf("Modes = %v, want %v", meta.Modes, want)
	}
	if len(meta.Categories) != 1 || meta.Categories[0] != core.CategoryTextGeneration {
		t.Errorf("Categories = %v, want [%v]", meta.Categories, core.CategoryTextGeneration)
	}

	// Only the true supports_* flags become capabilities, with the prefix
	// stripped; input_modalities ["text","image"] adds vision.
	wantCapabilities := map[string]bool{"reasoning": true, "prompt_caching": true, "vision": true}
	if len(meta.Capabilities) != len(wantCapabilities) {
		t.Errorf("Capabilities = %v, want %v", meta.Capabilities, wantCapabilities)
	}
	for name := range wantCapabilities {
		if !meta.Capabilities[name] {
			t.Errorf("Capabilities[%q] = false, want true", name)
		}
	}
	if meta.Capabilities["web_search"] || meta.Capabilities["function_calling"] {
		t.Errorf("Capabilities = %v, want false flags omitted", meta.Capabilities)
	}

	// 6e-8 USD/token -> $0.06/MTok, 1.8e-7 -> $0.18, 1.2e-8 -> $0.012.
	// The values come from `pricing`, not the higher `list_pricing` block.
	assertPrice(t, "InputPerMtok", meta.Pricing.InputPerMtok, 0.06)
	assertPrice(t, "OutputPerMtok", meta.Pricing.OutputPerMtok, 0.18)
	assertPrice(t, "CachedInputPerMtok", meta.Pricing.CachedInputPerMtok, 0.012)
	if meta.Pricing.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", meta.Pricing.Currency)
	}
}

// TestListModels_UsesDiscountedPricingNotListPricing pins the choice of block:
// cost metadata must describe what the account is actually charged.
func TestListModels_UsesDiscountedPricingNotListPricing(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[`+edenCatalogEntry+`]}`)
	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	if got := *model.Metadata.Pricing.InputPerMtok; got == 0.09 {
		t.Fatal("InputPerMtok took the list_pricing rate; want the discounted pricing block")
	}
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

	if model.Metadata == nil || model.Metadata.Pricing == nil {
		t.Fatal("Pricing = nil, want the list_pricing fallback")
	}
	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.09)
	assertPrice(t, "OutputPerMtok", model.Metadata.Pricing.OutputPerMtok, 0.28)
}

// TestListModels_ListPricingDoesNotMaskApplicablePricing asserts the fallback
// never overrides a usable applicable rate, including a partial one: a model
// priced only for input keeps that rate rather than swapping in the full list
// card.
func TestListModels_ListPricingDoesNotMaskApplicablePricing(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"pricing": {"input_cost_per_token": 6e-8},
		"list_pricing": {"input_cost_per_token": 9e-8, "output_cost_per_token": 2.8e-7}
	}]}`)

	assertPrice(t, "InputPerMtok", model.Metadata.Pricing.InputPerMtok, 0.06)
	if model.Metadata.Pricing.OutputPerMtok != nil {
		t.Errorf("OutputPerMtok = %v, want nil: the list card must not fill gaps in applicable pricing",
			*model.Metadata.Pricing.OutputPerMtok)
	}
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
				if model.Metadata != nil && model.Metadata.Pricing != nil {
					t.Fatalf("Pricing = %+v, want nil", model.Metadata.Pricing)
				}
				return
			}
			if model.Metadata == nil || model.Metadata.Pricing == nil {
				t.Fatal("Pricing = nil, want a partial pricing block")
			}
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
		if _, ok := perMtok(&rate); ok {
			t.Errorf("perMtok(%v) reported a usable price, want rejected", rate)
		}
	}
	if _, ok := perMtok(nil); ok {
		t.Error("perMtok(nil) reported a usable price, want rejected")
	}
	value, ok := perMtok(new(6e-8))
	if !ok || math.Abs(value-0.06) > 1e-9 {
		t.Errorf("perMtok(6e-8) = %v, %v; want 0.06, true", value, ok)
	}
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
			if !slices.Equal(modes, tt.wantModes) {
				t.Errorf("Modes = %v, want %v", modes, tt.wantModes)
			}
			for _, capability := range tt.wantCapabilities {
				if model.Metadata == nil || !model.Metadata.Capabilities[capability] {
					t.Errorf("Capabilities missing %q", capability)
				}
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
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("models = %+v, want the blank ID dropped", resp.Data)
	}
	if resp.Data[0].ID != "openai/gpt-4" || resp.Data[0].Metadata != nil {
		t.Errorf("bare entry = %+v, want nil Metadata", resp.Data[0])
	}
	if resp.Data[1].Object != "model" {
		t.Errorf("Object = %q, want the default \"model\" applied", resp.Data[1].Object)
	}
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

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatalf("ListModels() error = nil, want the upstream failure; resp = %+v", resp)
	}
	if resp != nil {
		t.Errorf("ListModels() resp = %+v, want nil on error", resp)
	}
}

func assertPrice(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %v", name, want)
		return
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}

func assertOptionalPrice(t *testing.T, name string, got, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s = %v, want nil (an unreported rate must not be costed)", name, *got)
		}
		return
	}
	assertPrice(t, name, got, *want)
}
