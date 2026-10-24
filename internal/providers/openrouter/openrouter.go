package openrouter

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultSiteURL = "https://gomodel.enterpilot.io"
	defaultAppName = "GoModel"
)

var Registration = providers.Registration{
	Type:                        "openrouter",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

type Provider struct {
	*openai.CompatibleProvider
	siteURL string
	appName string
}

// OpenRouter's /audio/speech and /audio/transcriptions endpoints are
// OpenAI-shaped, so the embedded CompatibleProvider implementation serves them
// as-is; this assertion keeps the audio surface (and the catalog's audio-only
// models, which the registry hides for providers without it) from silently
// disappearing if the embedding changes.
var _ core.AudioProvider = (*Provider)(nil)

func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	p := &Provider{
		siteURL: envOrDefault("OPENROUTER_SITE_URL", defaultSiteURL),
		appName: envOrDefault("OPENROUTER_APP_NAME", defaultAppName),
	}
	p.CompatibleProvider = openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "openrouter",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	})
	p.SetRequestMutator(p.mutateRequest)
	return p
}

func (p *Provider) mutateRequest(req *llmclient.Request) {
	if req.Headers == nil {
		req.Headers = make(http.Header)
	}
	if strings.TrimSpace(headerValue(req.Headers, "HTTP-Referer")) == "" && strings.TrimSpace(p.siteURL) != "" {
		req.Headers.Set("HTTP-Referer", p.siteURL)
	}
	if strings.TrimSpace(headerValue(req.Headers, "X-OpenRouter-Title")) == "" &&
		strings.TrimSpace(headerValue(req.Headers, "X-Title")) == "" &&
		strings.TrimSpace(p.appName) != "" {
		req.Headers.Set("X-OpenRouter-Title", p.appName)
	}
}

// openrouterModel is the subset of OpenRouter's /models entry the gateway
// reads beyond the OpenAI-compatible shape: per-model architecture modalities
// and context length, which the generic listing parser would drop.
type openrouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Created       int64  `json:"created"`
	ContextLength int    `json:"context_length"`
	Architecture  struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	// SupportedParameters lists the request parameters the model accepts
	// (tools, reasoning, response_format, structured_outputs, ...).
	SupportedParameters []string `json:"supported_parameters"`
	// Pricing holds OpenRouter's per-token USD rates as decimal strings.
	// "-1" marks a rate OpenRouter cannot state up front (auto-routed models).
	// Image is the rate per input image.
	Pricing struct {
		Prompt            string `json:"prompt"`
		Completion        string `json:"completion"`
		InputCacheRead    string `json:"input_cache_read"`
		InputCacheWrite   string `json:"input_cache_write"`
		InternalReasoning string `json:"internal_reasoning"`
		Image             string `json:"image"`
	} `json:"pricing"`
}

// ListModels parses OpenRouter's native models listing so architecture
// modalities and context length survive into model metadata. OpenRouter's
// catalog is far larger than the remote model registry, so discovery-time
// classification keeps its long tail categorized; registry enrichment and
// operator config still override the stamp.
//
// output_modalities=all is required: the endpoint defaults to text-output
// models only, which would hide OpenRouter's embedding and audio models from
// the catalog. Models whose every modality maps outside the gateway's
// OpenRouter surface (rerank-only, video-only) are skipped so the catalog
// never advertises a model that can only fail.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream struct {
		Data []openrouterModel `json:"data"`
	}
	if err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models?output_modalities=all",
	}, &upstream); err != nil {
		return nil, err
	}
	models := make([]core.Model, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || !openrouterServable(m) {
			continue
		}
		models = append(models, core.Model{
			ID:       id,
			Object:   "model",
			OwnedBy:  "openrouter",
			Created:  m.Created,
			Metadata: openrouterMetadata(m),
		})
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

// servableOpenRouterModalities are output modalities the gateway can reach on
// OpenRouter: text and image generation flow through chat completions,
// embeddings through /embeddings, speech/transcription through the /audio
// endpoints, and decisions (System One models such as Jev) through
// /v1/systemone. A model listing none of these (rerank-only, video) has no
// working endpoint here.
var servableOpenRouterModalities = map[string]struct{}{
	"text":          {},
	"image":         {},
	"embeddings":    {},
	"speech":        {},
	"transcription": {},
	"decisions":     {},
}

func openrouterServable(m openrouterModel) bool {
	// Missing architecture info means no signal, not proof of unservability;
	// keep the model rather than hiding it.
	if len(m.Architecture.OutputModalities) == 0 {
		return true
	}
	for _, modality := range m.Architecture.OutputModalities {
		if _, ok := servableOpenRouterModalities[strings.ToLower(strings.TrimSpace(modality))]; ok {
			return true
		}
	}
	return false
}

// openrouterMetadata maps output modalities onto gateway modes. Only the
// unambiguous mappings are claimed; anything else is left for the registry or
// ID inference.
func openrouterMetadata(m openrouterModel) *core.ModelMetadata {
	modes := make([]string, 0, 2)
	decisions := false
	// "rerank" is deliberately not mapped: the gateway has no rerank surface
	// on OpenRouter, and the rerank mode would sort the model into the
	// Embeddings category despite being unreachable here.
	for _, modality := range m.Architecture.OutputModalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "text":
			modes = append(modes, "chat")
		case "image":
			modes = append(modes, "image_generation")
		case "embeddings":
			modes = append(modes, "embedding")
		case "speech":
			modes = append(modes, "audio_speech")
		case "transcription":
			modes = append(modes, "audio_transcription")
		case "decisions":
			decisions = true
		}
	}
	pricing := openrouterPricing(m)
	capabilities := openrouterCapabilities(m)
	meta := &core.ModelMetadata{
		DisplayName:  strings.TrimSpace(m.Name),
		Description:  strings.TrimSpace(m.Description),
		Capabilities: capabilities,
		Pricing:      pricing,
	}
	switch {
	case len(modes) > 0:
		meta.Modes = modes
		meta.Categories = core.CategoriesForModes(modes)
	case decisions:
		// A decision model answers System One requests only. It has no
		// generation mode to claim, so like the jev provider's models it is
		// a utility model that no OpenAI endpoint routes to.
		meta.Categories = []core.ModelCategory{core.CategoryUtility}
	}
	if m.ContextLength > 0 {
		contextWindow := m.ContextLength
		meta.ContextWindow = &contextWindow
	}
	if m.TopProvider.MaxCompletionTokens > 0 {
		meta.MaxOutputTokens = new(m.TopProvider.MaxCompletionTokens)
	}
	if len(meta.Categories) == 0 && meta.ContextWindow == nil && meta.MaxOutputTokens == nil && pricing == nil &&
		capabilities == nil && meta.DisplayName == "" && meta.Description == "" {
		return nil
	}
	return meta
}

// openrouterCapabilities maps the request parameters a model accepts and the
// input modalities it takes onto the catalog's capability keys. Sampling
// knobs (temperature, top_p, ...) are not capabilities and are skipped.
func openrouterCapabilities(m openrouterModel) map[string]bool {
	var capabilities map[string]bool
	for _, parameter := range m.SupportedParameters {
		switch strings.ToLower(strings.TrimSpace(parameter)) {
		case "tools", "tool_choice", "parallel_tool_calls", "reasoning", "structured_outputs", "response_format", "web_search_options":
			capabilities = providers.SetCapability(capabilities, parameter, true)
		}
	}
	return providers.CapabilitiesFromInputModalities(capabilities, m.Architecture.InputModalities)
}

// openrouterPricing converts OpenRouter's per-token rates into the gateway's
// per-million-token pricing. OpenRouter prices each model itself, including the
// ":free" variants it publishes at zero, so its own listing is more current
// than the remote model registry for this catalog — and enrichment already
// treats what a provider reports as the override.
func openrouterPricing(m openrouterModel) *core.ModelPricing {
	input, hasInput := perMtok(m.Pricing.Prompt)
	output, hasOutput := perMtok(m.Pricing.Completion)
	if !hasInput && !hasOutput {
		return nil
	}
	pricing := &core.ModelPricing{Currency: "USD"}
	if hasInput {
		pricing.InputPerMtok = &input
	}
	if hasOutput {
		pricing.OutputPerMtok = &output
	}
	if rate, ok := perMtok(m.Pricing.InputCacheRead); ok && m.Pricing.InputCacheRead != "" {
		pricing.CachedInputPerMtok = &rate
	}
	if rate, ok := perMtok(m.Pricing.InputCacheWrite); ok && m.Pricing.InputCacheWrite != "" {
		pricing.CacheWritePerMtok = &rate
	}
	if rate, ok := perMtok(m.Pricing.InternalReasoning); ok && m.Pricing.InternalReasoning != "" {
		pricing.ReasoningOutputPerMtok = &rate
	}
	// The image rate is per input image, not per token, so it is not scaled.
	if rate, ok := perMtok(m.Pricing.Image); ok && m.Pricing.Image != "" && rate > 0 {
		perImage := rate / 1_000_000
		pricing.InputPerImage = &perImage
	}
	return pricing
}

// perMtok parses a per-token USD rate and scales it to per million tokens;
// see providers.PerTokenRateToMtok for the rejected inputs.
func perMtok(rate string) (float64, bool) {
	return providers.PerTokenRateToMtok(rate)
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:        "Bearer ",
		RequestIDHeader:   "X-Client-Request-Id",
		ValidateRequestID: providers.IsValidClientRequestID,
	})
	// OpenRouter uses this value to keep a conversation on the same resolved
	// model and provider endpoint from its first successful request, maximizing
	// upstream prompt-cache reuse. GoModel's session detector already scopes
	// user-supplied IDs before placing them in context.
	if sessionID := strings.TrimSpace(core.SessionIDFromContext(req.Context())); sessionID != "" {
		req.Header.Set("X-Session-Id", sessionID)
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func headerValue(headers http.Header, key string) string {
	for existingKey, values := range headers {
		if !strings.EqualFold(existingKey, key) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}
