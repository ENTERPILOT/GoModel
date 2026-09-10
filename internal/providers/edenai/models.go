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

// modelPricing holds one of Eden's per-token USD rate blocks.
type modelPricing struct {
	InputCostPerToken       *float64 `json:"input_cost_per_token"`
	OutputCostPerToken      *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost *float64 `json:"cache_read_input_token_cost"`
}

// effectivePricing picks the rate card to publish. Eden's `pricing` is what
// the account is actually charged (any discount already applied), so it wins
// whenever it carries a usable rate. `list_pricing` is the undiscounted card
// and is used only as a fallback: an approximate rate still lets price
// filters and the cost load-balancing strategy rank the model, whereas no
// rate at all drops it from both.
func (m modelInfo) effectivePricing() *core.ModelPricing {
	if pricing := m.Pricing.toCore(); pricing != nil {
		return pricing
	}
	return m.ListPricing.toCore()
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

// toCore converts one of Eden's per-token USD rate blocks into the gateway's
// per-million-token pricing. Eden names the unit in each field
// (input_cost_per_token), so the ×1e6 scaling is read off the contract rather
// than assumed. A rate Eden omits stays absent: costing a token type at zero
// because no price was published would understate spend, so only a rate Eden
// explicitly reports as 0 prices at zero.
func (p *modelPricing) toCore() *core.ModelPricing {
	if p == nil {
		return nil
	}
	input, hasInput := perMtok(p.InputCostPerToken)
	output, hasOutput := perMtok(p.OutputCostPerToken)
	cachedInput, hasCachedInput := perMtok(p.CacheReadInputTokenCost)
	if !hasInput && !hasOutput && !hasCachedInput {
		return nil
	}

	pricing := &core.ModelPricing{Currency: "USD"}
	if hasInput {
		pricing.InputPerMtok = &input
	}
	if hasOutput {
		pricing.OutputPerMtok = &output
	}
	if hasCachedInput {
		pricing.CachedInputPerMtok = &cachedInput
	}
	return pricing
}

// perMtok scales one per-token USD rate to per million tokens. Rates that are
// absent, negative, or non-finite report no price rather than a wrong one, and
// so does a rate large enough that scaling overflows to infinity: a corrupt
// number here would propagate into every price comparison, budget total, and
// cost-strategy decision downstream.
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
