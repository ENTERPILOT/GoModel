package telemetry

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/enterpilot/gomodel/internal/core"
)

// outcomeAttributes describes what the gateway decoded from a call: response
// identity, finish reasons, and token usage, plus the exchanged messages
// when content capture is on. Usage is exported as the gateway normalized it,
// the same numbers usage tracking records.
func (o *observer) outcomeAttributes(outcome core.GenerationOutcome) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	switch resp := outcome.Response.(type) {
	case *core.ChatResponse:
		if resp != nil {
			attrs = responseAttributes(resp.ID, resp.Model, chatFinishReasons(resp))
			attrs = appendUsage(attrs, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
		}
	case *core.ResponsesResponse:
		if resp != nil {
			attrs = responseAttributes(resp.ID, resp.Model, responsesFinishReasons(resp))
			if resp.Usage != nil {
				attrs = appendUsage(attrs, resp.Usage.InputTokens, resp.Usage.OutputTokens)
			}
		}
	}
	if o.captureContent {
		attrs = append(attrs, contentAttributes(outcome)...)
	}
	return attrs
}

func responseAttributes(id, model string, finishReasons []string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 5)
	if id = strings.TrimSpace(id); id != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.id", id))
	}
	if model = modelName(model); model != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.model", model))
	}
	if len(finishReasons) > 0 {
		attrs = append(attrs, attribute.StringSlice("gen_ai.response.finish_reasons", finishReasons))
	}
	return attrs
}

// appendUsage exports token counts only when the provider reported any, so a
// response without usage is not shown as a free call.
func appendUsage(attrs []attribute.KeyValue, inputTokens, outputTokens int) []attribute.KeyValue {
	if inputTokens <= 0 && outputTokens <= 0 {
		return attrs
	}
	return append(attrs,
		attribute.Int("gen_ai.usage.input_tokens", max(inputTokens, 0)),
		attribute.Int("gen_ai.usage.output_tokens", max(outputTokens, 0)),
	)
}

func chatFinishReasons(resp *core.ChatResponse) []string {
	var reasons []string
	for _, choice := range resp.Choices {
		if reason := strings.TrimSpace(choice.FinishReason); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

func responsesFinishReasons(resp *core.ResponsesResponse) []string {
	if reason := responsesFinishReason(resp); reason != "" {
		return []string{reason}
	}
	return nil
}

// responsesFinishReason maps a Responses API status onto a chat-style finish
// reason: a completed response stopped, an incomplete one names its cause,
// and a background response still running has none yet.
func responsesFinishReason(resp *core.ResponsesResponse) string {
	switch status := strings.TrimSpace(resp.Status); status {
	case "completed":
		return "stop"
	case "incomplete":
		if resp.IncompleteDetails != nil && strings.TrimSpace(resp.IncompleteDetails.Reason) != "" {
			return strings.TrimSpace(resp.IncompleteDetails.Reason)
		}
		return status
	case "", "queued", "in_progress":
		return ""
	default:
		return status
	}
}
