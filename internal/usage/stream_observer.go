package usage

import "gomodel/internal/core"

// StreamUsageObserver extracts usage data from parsed SSE JSON payloads.
type StreamUsageObserver struct {
	logger          LoggerInterface
	pricingResolver PricingResolver
	cachedEntry     *UsageEntry
	model           string
	provider        string
	requestID       string
	endpoint        string
	closed          bool
}

func NewStreamUsageObserver(logger LoggerInterface, model, provider, requestID, endpoint string, pricingResolver PricingResolver) *StreamUsageObserver {
	if logger == nil {
		return nil
	}
	return &StreamUsageObserver{
		logger:          logger,
		pricingResolver: pricingResolver,
		model:           model,
		provider:        provider,
		requestID:       requestID,
		endpoint:        endpoint,
	}
}

func (o *StreamUsageObserver) OnJSONEvent(chunk map[string]interface{}) {
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

func (o *StreamUsageObserver) extractUsageFromEvent(chunk map[string]interface{}) *UsageEntry {
	providerID, _ := chunk["id"].(string)

	model := o.model
	if m, ok := chunk["model"].(string); ok && m != "" {
		model = m
	}

	usageRaw, ok := chunk["usage"]
	if !ok {
		if eventType, _ := chunk["type"].(string); eventType == "response.completed" || eventType == "response.done" {
			if response, respOK := chunk["response"].(map[string]interface{}); respOK {
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

	usageMap, ok := usageRaw.(map[string]interface{})
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

	for field := range extendedFieldSet {
		if v, ok := usageMap[field].(float64); ok && v > 0 {
			rawData[field] = int(v)
		}
	}

	if details, ok := usageMap["prompt_tokens_details"].(map[string]interface{}); ok {
		for k, v := range details {
			if fv, ok := v.(float64); ok && fv > 0 {
				rawData["prompt_"+k] = int(fv)
			}
		}
	}
	if details, ok := usageMap["completion_tokens_details"].(map[string]interface{}); ok {
		for k, v := range details {
			if fv, ok := v.(float64); ok && fv > 0 {
				rawData["completion_"+k] = int(fv)
			}
		}
	}

	if inputTokens == 0 && outputTokens == 0 && totalTokens == 0 {
		return nil
	}
	if len(rawData) == 0 {
		rawData = nil
	}

	var pricingArgs []*core.ModelPricing
	if o.pricingResolver != nil {
		if p := o.pricingResolver.ResolvePricing(model, o.provider); p != nil {
			pricingArgs = append(pricingArgs, p)
		}
	}

	return ExtractFromSSEUsage(
		providerID,
		inputTokens, outputTokens, totalTokens,
		rawData,
		o.requestID, model, o.provider, o.endpoint,
		pricingArgs...,
	)
}
