package edenai

import (
	"context"
	"math"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// usdPerTokenToPerMtok scales Eden's per-token USD rates to the gateway's
// per-million-token representation. Eden names the unit in the field itself
// (input_cost_per_token), so the conversion is fixed rather than inferred.
const usdPerTokenToPerMtok = 1_000_000

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

// modelInfo is one entry of Eden's /models catalog. Capabilities is decoded
// as a loose map on purpose: Eden publishes a growing set of supports_* flags,
// and a map keeps new ones flowing through without a source change here.
type modelInfo struct {
	ID            string         `json:"id"`
	Object        string         `json:"object"`
	OwnedBy       string         `json:"owned_by"`
	Description   string         `json:"description"`
	Capabilities  map[string]any `json:"capabilities"`
	Pricing       *modelPricing  `json:"pricing"`
	ListPricing   *modelPricing  `json:"list_pricing"`
	Created       int64          `json:"created"`
	ContextLength int            `json:"context_length"`
}

// modelPricing holds one of Eden's per-token USD rate blocks. Only the rates
// whose unit and meaning both map onto a core.ModelPricing field are decoded.
//
// Left unread on purpose:
//   - Context-length-tiered variants (input_cost_per_token_above_200k_tokens),
//     the tiered_pricing list, and per-query search fees (an object, not a
//     scalar). The gateway's pricing model has no equivalent, so reading them
//     into a flat per-Mtok field would misprice the model.
//   - output_cost_per_reasoning_token and input_cost_per_audio_token. The rates
//     exist in Eden's catalog, but the gateway would apply them through usage's
//     OpenAI-compatible mappings, which treat reasoning and audio counts as
//     already included in the base completion/prompt totals and subtract the
//     base rate accordingly. Eden's documented usage object carries only
//     prompt_tokens, completion_tokens, and total_tokens -- no reasoning or
//     audio breakdown -- so there is nothing to confirm that assumption
//     against. Mapping them would stake cost math on an unverified split for
//     token counts Eden does not currently report.
type modelPricing struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

// pricingRate pairs one Eden per-token rate with the core.ModelPricing field
// it feeds, so effectivePricing can resolve every rate the same way instead of
// repeating the fallback per field. The shape follows usage.tokenCostMapping,
// which already expresses the same read-field/write-field pairing.
var pricingRates = []struct {
	rate   func(*modelPricing) *float64
	assign func(*core.ModelPricing, float64)
}{
	{
		func(p *modelPricing) *float64 { return p.InputCostPerToken },
		func(c *core.ModelPricing, v float64) { c.InputPerMtok = &v },
	},
	{
		func(p *modelPricing) *float64 { return p.OutputCostPerToken },
		func(c *core.ModelPricing, v float64) { c.OutputPerMtok = &v },
	},
	{
		func(p *modelPricing) *float64 { return p.CacheReadInputTokenCost },
		func(c *core.ModelPricing, v float64) { c.CachedInputPerMtok = &v },
	},
	{
		func(p *modelPricing) *float64 { return p.CacheCreationInputTokenCost },
		func(c *core.ModelPricing, v float64) { c.CacheWritePerMtok = &v },
	},
}

// effectivePricing builds the rate card to publish, resolving each rate
// independently.
//
// Eden's `pricing` is what the account is actually charged — the undiscounted
// `list_pricing` with the account discount already applied — so a usable
// account rate always wins, including an explicit 0 for a genuinely free
// model. `list_pricing` fills in only the individual rates `pricing` leaves
// unusable. The fallback is per field rather than per block so a block that
// prices some token types and not others keeps its account rates instead of
// losing the rates it does not carry.
//
// Eden currently publishes the same key set in both blocks, so in practice
// every rate resolves from `pricing`; the per-field path is what keeps that
// from being load-bearing. A rate perMtok rejects as unusable (negative,
// non-finite, overflowing) is treated the same as an absent one and may fall
// back, since an approximate rate still lets price filters and the cost
// load-balancing strategy rank the model, whereas no rate at all drops it
// from both.
func (m modelInfo) effectivePricing() *core.ModelPricing {
	pricing := &core.ModelPricing{Currency: "USD"}
	priced := false
	for _, mapping := range pricingRates {
		rate, ok := perMtok(rateFrom(m.Pricing, mapping.rate))
		if !ok {
			rate, ok = perMtok(rateFrom(m.ListPricing, mapping.rate))
		}
		if !ok {
			continue
		}
		mapping.assign(pricing, rate)
		priced = true
	}
	if !priced {
		return nil
	}
	return pricing
}

// rateFrom reads one rate out of a rate block Eden may have omitted entirely.
func rateFrom(block *modelPricing, field func(*modelPricing) *float64) *float64 {
	if block == nil {
		return nil
	}
	return field(block)
}

// ListModels returns Eden's live catalog, retaining the context window,
// capability flags, modalities, and per-token pricing Eden publishes with it.
// Eden's /models response is the source of truth for this provider: no static
// list is kept here, so a model Eden adds appears at the next registry
// refresh without a source change.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	if err := p.compat.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, err
	}

	result := &core.ModelsResponse{Object: "list"}
	result.Data = make([]core.Model, 0, len(upstream.Data))
	for _, model := range upstream.Data {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		result.Data = append(result.Data, model.toCore())
	}
	return result, nil
}

// toCore normalizes an Eden catalog entry into GoModel's provider-neutral
// model shape.
func (m modelInfo) toCore() core.Model {
	object := strings.TrimSpace(m.Object)
	if object == "" {
		object = "model"
	}

	metadata := &core.ModelMetadata{
		Description:  strings.TrimSpace(m.Description),
		Capabilities: m.capabilities(),
		Pricing:      m.effectivePricing(),
	}
	if modes := m.modes(); len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	if metadataEmpty(metadata) {
		metadata = nil
	}

	return core.Model{
		ID:       strings.TrimSpace(m.ID),
		Object:   object,
		OwnedBy:  strings.TrimSpace(m.OwnedBy),
		Created:  m.Created,
		Metadata: metadata,
	}
}

// metadataEmpty reports whether an entry contributed nothing worth attaching,
// so a bare catalog row leaves Metadata nil rather than an empty struct that
// enrichment would treat as a real provider report.
func metadataEmpty(metadata *core.ModelMetadata) bool {
	return metadata.Description == "" &&
		len(metadata.Capabilities) == 0 &&
		metadata.Pricing == nil &&
		len(metadata.Modes) == 0 &&
		metadata.ContextWindow == nil
}

// modes maps Eden's output modalities onto the gateway's mode vocabulary.
// Text models claim both "chat" and "responses": the Responses surface is
// served for them by translating through chat completions. Modalities the
// gateway has no Eden-backed surface for still produce their mode, so the
// registry can hide models this provider cannot actually serve (it implements
// neither core.AudioProvider nor core.ImageProvider).
func (m modelInfo) modes() []string {
	modes := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(mode string) {
		if _, ok := seen[mode]; ok {
			return
		}
		seen[mode] = struct{}{}
		modes = append(modes, mode)
	}
	for _, modality := range stringSlice(m.Capabilities["output_modalities"]) {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "text":
			add("chat")
			add("responses")
		case "image":
			add("image_generation")
		case "audio", "speech":
			add("audio_speech")
		case "embedding", "embeddings":
			add("embedding")
		case "video":
			add("video_generation")
		}
	}
	if len(modes) == 0 {
		return nil
	}
	return modes
}

// capabilities projects Eden's supports_* flags and input modalities into the
// gateway's capability map. The supports_ prefix is stripped so the names read
// the same way other providers report them ("function_calling", "reasoning").
// Unknown keys are ignored rather than guessed at, but any future supports_*
// flag is picked up automatically.
func (m modelInfo) capabilities() map[string]bool {
	capabilities := make(map[string]bool, len(m.Capabilities))
	for key, value := range m.Capabilities {
		name, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(key)), "supports_")
		if !ok || name == "" {
			continue
		}
		enabled, ok := value.(bool)
		if !ok || !enabled {
			continue
		}
		capabilities[name] = true
	}
	for _, modality := range stringSlice(m.Capabilities["input_modalities"]) {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			capabilities["vision"] = true
		case "audio":
			capabilities["audio"] = true
		case "video":
			capabilities["video"] = true
		}
	}
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}

// stringSlice reads a JSON string array out of the loosely decoded
// capabilities map, skipping non-string members.
func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// perMtok scales one per-token USD rate to per million tokens. Eden names the
// unit in each field (input_cost_per_token), so the x1e6 scaling is read off
// the contract rather than assumed.
//
// Rates that are absent, negative, or non-finite report no price rather than a
// wrong one, and so does a rate large enough that scaling overflows to
// infinity: a corrupt number here would propagate into every price comparison,
// budget total, and cost-strategy decision downstream. A rate Eden explicitly
// reports as 0 is a real price and is kept -- costing a token type at zero
// because no price was published would understate spend, but a published zero
// means free.
func perMtok(perToken *float64) (float64, bool) {
	if perToken == nil {
		return 0, false
	}
	rate := *perToken
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	scaled := rate * usdPerTokenToPerMtok
	if math.IsInf(scaled, 0) {
		return 0, false
	}
	return scaled, true
}
