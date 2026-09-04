package providers

import (
	"context"
	"fmt"
	"slices"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const anthropicCacheControlField = "cache_control"

// anthropicOnlyMessageFields are message extras the Anthropic ingress sets for
// the Anthropic provider alone. Every other provider forwards message extras
// verbatim and would reject or misread them.
var anthropicOnlyMessageFields = []string{core.ThinkingBlocksField, core.ToolResultIsErrorField}

// adaptAnthropicCacheControl removes Anthropic-only cache directives and
// message extras after the route is known unless the selected provider
// accepts Anthropic request shapes. The caller's request remains unchanged.
func adaptAnthropicCacheControl(req *core.ChatRequest, providerType string) *core.ChatRequest {
	if req == nil {
		return req
	}
	stripCacheControl := !providerAcceptsAnthropicCacheControl(providerType) && hasAnthropicCacheControl(req)
	stripMessageFields := normalizedProviderType(providerType) != "anthropic" && hasAnthropicOnlyMessageFields(req)
	if !stripCacheControl && !stripMessageFields {
		return req
	}

	adapted := *req
	if stripCacheControl {
		adapted.ExtraFields = req.ExtraFields.Without(anthropicCacheControlField)

		adapted.Tools = make([]map[string]any, len(req.Tools))
		for i, tool := range req.Tools {
			cloned := make(map[string]any, len(tool))
			for key, value := range tool {
				if key != anthropicCacheControlField {
					cloned[key] = value
				}
			}
			adapted.Tools[i] = cloned
		}
	}

	adapted.Messages = make([]core.Message, len(req.Messages))
	for i, message := range req.Messages {
		if stripCacheControl {
			message = withoutAnthropicMessageCacheControl(message)
		}
		if stripMessageFields {
			message.ExtraFields = message.ExtraFields.Without(anthropicOnlyMessageFields...)
		}
		adapted.Messages[i] = message
	}
	return &adapted
}

func hasAnthropicOnlyMessageFields(req *core.ChatRequest) bool {
	return slices.ContainsFunc(req.Messages, func(message core.Message) bool {
		for _, field := range anthropicOnlyMessageFields {
			if len(message.ExtraFields.Lookup(field)) > 0 {
				return true
			}
		}
		return false
	})
}

// adaptAnthropicBatchCacheControl applies the same post-routing policy to
// canonical chat items produced by the Anthropic Message Batches ingress.
// Ordinary OpenAI-compatible batches remain opaque and caller-owned.
func adaptAnthropicBatchCacheControl(ctx context.Context, req *core.BatchRequest, providerType string) (*core.BatchRequest, error) {
	if req == nil || core.RequestDialectFromContext(ctx) != core.RequestDialectAnthropicMessages {
		return req, nil
	}
	// Only Anthropic itself accepts every Anthropic extra; OpenRouter accepts
	// cache_control but still needs the message-level extras removed.
	if providerAcceptsAnthropicCacheControl(providerType) && normalizedProviderType(providerType) == "anthropic" {
		return req, nil
	}

	adapted := *req
	adapted.Requests = append([]core.BatchRequestItem(nil), req.Requests...)
	for i, item := range req.Requests {
		decoded, err := core.DecodeKnownBatchItemRequest(req.Endpoint, item)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("requests[%d]: %v", i, err), err)
		}
		chat, ok := decoded.Request.(*core.ChatRequest)
		if !ok {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("requests[%d]: Anthropic Message Batch item is not a chat completion", i), nil,
			)
		}
		body, err := json.Marshal(adaptAnthropicCacheControl(chat, providerType))
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("requests[%d]: failed to encode adapted chat request", i), err)
		}
		adapted.Requests[i].Body = body
	}
	return &adapted, nil
}

func providerAcceptsAnthropicCacheControl(providerType string) bool {
	return promptCacheProfileFor(providerType).acceptsAnthropicCacheControl
}

func hasAnthropicCacheControl(req *core.ChatRequest) bool {
	if len(req.ExtraFields.Lookup(anthropicCacheControlField)) > 0 {
		return true
	}
	for _, tool := range req.Tools {
		if _, ok := tool[anthropicCacheControlField]; ok {
			return true
		}
	}
	return slices.ContainsFunc(req.Messages, messageHasAnthropicCacheControl)
}

func messageHasAnthropicCacheControl(message core.Message) bool {
	if len(message.ExtraFields.Lookup(anthropicCacheControlField)) > 0 {
		return true
	}
	for _, call := range message.ToolCalls {
		if len(call.ExtraFields.Lookup(anthropicCacheControlField)) > 0 ||
			len(call.Function.ExtraFields.Lookup(anthropicCacheControlField)) > 0 {
			return true
		}
	}
	parts, ok := message.Content.([]core.ContentPart)
	if !ok {
		return false
	}
	for _, part := range parts {
		if len(part.ExtraFields.Lookup(anthropicCacheControlField)) > 0 ||
			(part.ImageURL != nil && len(part.ImageURL.ExtraFields.Lookup(anthropicCacheControlField)) > 0) ||
			(part.InputAudio != nil && len(part.InputAudio.ExtraFields.Lookup(anthropicCacheControlField)) > 0) ||
			(part.File != nil && len(part.File.ExtraFields.Lookup(anthropicCacheControlField)) > 0) {
			return true
		}
	}
	return false
}

func withoutAnthropicMessageCacheControl(message core.Message) core.Message {
	message.ExtraFields = message.ExtraFields.Without(anthropicCacheControlField)
	if message.ToolCalls != nil {
		message.ToolCalls = append([]core.ToolCall(nil), message.ToolCalls...)
		for i := range message.ToolCalls {
			message.ToolCalls[i].ExtraFields = message.ToolCalls[i].ExtraFields.Without(anthropicCacheControlField)
			message.ToolCalls[i].Function.ExtraFields = message.ToolCalls[i].Function.ExtraFields.Without(anthropicCacheControlField)
		}
	}

	parts, ok := message.Content.([]core.ContentPart)
	if !ok {
		return message
	}
	parts = append([]core.ContentPart(nil), parts...)
	for i := range parts {
		parts[i].ExtraFields = parts[i].ExtraFields.Without(anthropicCacheControlField)
		if parts[i].ImageURL != nil {
			image := *parts[i].ImageURL
			image.ExtraFields = image.ExtraFields.Without(anthropicCacheControlField)
			parts[i].ImageURL = &image
		}
		if parts[i].InputAudio != nil {
			audio := *parts[i].InputAudio
			audio.ExtraFields = audio.ExtraFields.Without(anthropicCacheControlField)
			parts[i].InputAudio = &audio
		}
		if parts[i].File != nil {
			file := *parts[i].File
			file.ExtraFields = file.ExtraFields.Without(anthropicCacheControlField)
			parts[i].File = &file
		}
	}
	message.Content = parts
	return message
}
