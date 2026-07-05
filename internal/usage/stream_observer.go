package usage

import (
	"bytes"
	"log/slog"
	"strings"

	"gomodel/internal/core"
)

// StreamUsageObserver extracts usage data from parsed SSE JSON payloads.
type StreamUsageObserver struct {
	logger          LoggerInterface
	pricingResolver PricingResolver
	cachedEntry     *UsageEntry
	model           string
	provider        string
	providerName    string
	requestID       string
	endpoint        string
	userPath        string
	labels          []string
	rewriteSaved    int
	closed          bool
}

func NewStreamUsageObserver(logger LoggerInterface, model, provider, requestID, endpoint string, pricingResolver PricingResolver, userPath ...string) *StreamUsageObserver {
	if logger == nil {
		return nil
	}
	normalizedUserPath := "/"
	if len(userPath) > 0 {
		if normalized, err := core.NormalizeUserPath(userPath[0]); err == nil {
			if normalized != "" {
				normalizedUserPath = normalized
			}
		} else {
			slog.Warn("stream usage observer received invalid user_path; using root fallback", "error", err)
			normalizedUserPath = "/"
		}
	}
	return &StreamUsageObserver{
		logger:          logger,
		pricingResolver: pricingResolver,
		model:           model,
		provider:        provider,
		requestID:       requestID,
		endpoint:        endpoint,
		userPath:        normalizedUserPath,
	}
}

func (o *StreamUsageObserver) SetProviderName(providerName string) {
	if o == nil {
		return
	}
	o.providerName = strings.TrimSpace(providerName)
}

// SetLabels attaches the request labels extracted from tagging headers.
func (o *StreamUsageObserver) SetLabels(labels []string) {
	if o == nil {
		return
	}
	o.labels = labels
}

// SetRewriteTokensSaved attaches the request's rewrite savings estimate so
// the usage entry extracted from the stream records it (with its cost, when
// pricing is resolvable).
func (o *StreamUsageObserver) SetRewriteTokensSaved(tokensSaved int) {
	if o == nil || tokensSaved <= 0 {
		return
	}
	o.rewriteSaved = tokensSaved
}

// usageKeyLiteral gates WantsJSONEvent: extractUsageFromEvent can only produce
// an entry from payloads carrying a "usage" member (top-level or nested under
// "response"), so events without the literal never matter.
var usageKeyLiteral = []byte(`"usage"`)

// WantsJSONEvent reports whether the raw SSE payload can carry usage data.
// This lets the observed stream skip JSON decoding for the vast majority of
// content-delta chunks. Known trade-off: a provider that JSON-escapes key
// characters (e.g. "usage") would slip past this byte scan; no known
// provider does, and covering it would mean decoding every chunk again.
func (o *StreamUsageObserver) WantsJSONEvent(raw []byte) bool {
	return bytes.Contains(raw, usageKeyLiteral)
}

func (o *StreamUsageObserver) OnJSONEvent(chunk map[string]any) {
	entry := o.extractUsageFromEvent(chunk)
	if entry != nil {
		o.cachedEntry = entry
	}
}

func (o *StreamUsageObserver) OnStreamClose() {
	if o.closed {
		return
	}
	o.closed = true
	if o.cachedEntry != nil && o.logger != nil {
		o.logger.Write(o.cachedEntry)
	}
}

func (o *StreamUsageObserver) extractUsageFromEvent(chunk map[string]any) *UsageEntry {
	providerID, _ := chunk["id"].(string)

	model := o.model
	if m, ok := chunk["model"].(string); ok && m != "" {
		model = m
	}

	usageRaw, ok := chunk["usage"]
	if !ok {
		if eventType, _ := chunk["type"].(string); eventType == "response.completed" || eventType == "response.done" {
			if response, respOK := chunk["response"].(map[string]any); respOK {
				usageRaw, ok = response["usage"]
				if id, idOK := response["id"].(string); idOK && id != "" {
					providerID = id
				}
				if m, modelOK := response["model"].(string); modelOK && m != "" {
					model = m
				}
			}
		}
	}
	if !ok {
		return nil
	}

	usageMap, ok := usageRaw.(map[string]any)
	if !ok {
		return nil
	}

	var inputTokens, outputTokens, totalTokens int
	rawData := make(map[string]any)

	if v, ok := usageMap["prompt_tokens"].(float64); ok {
		inputTokens = int(v)
	}
	if v, ok := usageMap["input_tokens"].(float64); ok {
		inputTokens = int(v)
	}
	if v, ok := usageMap["completion_tokens"].(float64); ok {
		outputTokens = int(v)
	}
	if v, ok := usageMap["output_tokens"].(float64); ok {
		outputTokens = int(v)
	}
	if v, ok := usageMap["total_tokens"].(float64); ok {
		totalTokens = int(v)
	}

	copyExtendedUsageFields(rawData, usageMap)
	copyUsageDetailsFields(rawData, usageMap["prompt_tokens_details"], "prompt_")
	copyUsageDetailsFields(rawData, usageMap["input_tokens_details"], "prompt_")
	copyUsageDetailsFields(rawData, usageMap["completion_tokens_details"], "completion_")
	copyUsageDetailsFields(rawData, usageMap["output_tokens_details"], "completion_")

	if inputTokens == 0 && outputTokens == 0 && totalTokens == 0 {
		return nil
	}
	if len(rawData) == 0 {
		rawData = nil
	}

	var pricingArgs []*core.ModelPricing
	if o.pricingResolver != nil {
		if p := o.pricingResolver.ResolvePricing(o.pricingModel(model), o.pricingProvider()); p != nil {
			pricingArgs = append(pricingArgs, p)
		}
	}

	entry := ExtractFromSSEUsage(
		providerID,
		inputTokens, outputTokens, totalTokens,
		rawData,
		o.requestID, model, o.provider, o.endpoint,
		pricingArgs...,
	)
	if entry != nil {
		entry.ProviderName = o.providerName
		entry.UserPath = o.userPath
		entry.Labels = o.labels
		var pricing *core.ModelPricing
		if len(pricingArgs) > 0 {
			pricing = pricingArgs[0]
		}
		ApplyRewriteSavings(entry, o.rewriteSaved, pricing)
	}
	return entry
}

func (o *StreamUsageObserver) pricingModel(responseModel string) string {
	if o == nil {
		return strings.TrimSpace(responseModel)
	}
	if model := strings.TrimSpace(o.model); model != "" {
		return model
	}
	return strings.TrimSpace(responseModel)
}

func (o *StreamUsageObserver) pricingProvider() string {
	if o == nil {
		return ""
	}
	if providerName := strings.TrimSpace(o.providerName); providerName != "" {
		return providerName
	}
	return strings.TrimSpace(o.provider)
}

func copyExtendedUsageFields(rawData map[string]any, usageMap map[string]any) {
	for key, value := range usageMap {
		switch key {
		case "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens",
			"prompt_tokens_details", "completion_tokens_details", "input_tokens_details", "output_tokens_details":
			continue
		case "cost", "cost_in_usd_ticks":
			if numericValue, ok := numericFloat(value); ok && numericValue >= 0 {
				rawData[key] = numericValue
			}
			continue
		case "cost_details":
			if details, ok := nestedUsageMap(value); ok {
				rawData[key] = details
			}
			continue
		case "is_byok":
			if boolValue, ok := value.(bool); ok {
				rawData[key] = boolValue
			}
			continue
		}
		if numericValue, ok := numericUsageValue(value); ok && numericValue > 0 {
			rawData[key] = numericValue
		}
	}
}

func copyUsageDetailsFields(rawData map[string]any, detailsValue any, prefix string) {
	details, ok := detailsValue.(map[string]any)
	if !ok {
		return
	}

	for key, value := range details {
		numericValue, ok := numericUsageValue(value)
		if !ok || numericValue <= 0 {
			continue
		}

		switch key {
		case "cache_read_input_tokens", "cache_creation_input_tokens":
			rawData[key] = numericValue
		default:
			rawData[prefix+key] = numericValue
		}
	}
}

func numericUsageValue(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}
