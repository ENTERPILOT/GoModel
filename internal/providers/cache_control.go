package providers

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const anthropicCacheControlField = "cache_control"

// adaptAnthropicCacheControl removes Anthropic cache directives after the
// route is known unless the selected provider accepts them. The caller's
// request remains unchanged. Anthropic-only message state (thinking blocks,
// is_error) travels under extra_content and is handled by adaptExtraContent.
func adaptAnthropicCacheControl(req *core.ChatRequest, providerType string) *core.ChatRequest {
	if req == nil || providerAcceptsAnthropicCacheControl(providerType) || !hasAnthropicCacheControl(req) {
		return req
	}

	adapted := *req
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

	adapted.Messages = make([]core.Message, len(req.Messages))
	for i, message := range req.Messages {
		adapted.Messages[i] = withoutAnthropicMessageCacheControl(message)
	}
	return &adapted
}

// adaptBatchRequest applies the post-routing policy to batch items. Items
// from the Anthropic Message Batches ingress get the cache-directive and
// extra_content treatment of a chat request. Ordinary OpenAI-compatible
// batches stay opaque and caller-owned except for foreign extra_content,
// which is removed from the chat and Responses items that carry it. The
// request is returned as-is when no item changes.
func adaptBatchRequest(ctx context.Context, req *core.BatchRequest, providerType string) (*core.BatchRequest, error) {
	if req == nil {
		return req, nil
	}
	anthropicDialect := core.RequestDialectFromContext(ctx) == core.RequestDialectAnthropicMessages

	adapted := *req
	adapted.Requests = append([]core.BatchRequestItem(nil), req.Requests...)
	changed := false
	for i, item := range req.Requests {
		if !anthropicDialect && !bytes.Contains(item.Body, []byte(core.ExtraContentField)) {
			continue
		}
		decoded, err := core.DecodeKnownBatchItemRequest(req.Endpoint, item)
		if err != nil {
			if anthropicDialect {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("requests[%d]: %v", i, err), err)
			}
			continue
		}
		var forward any
		switch request := decoded.Request.(type) {
		case *core.ChatRequest:
			chat := adaptExtraContent(request, providerType)
			if anthropicDialect {
				chat = adaptAnthropicCacheControl(chat, providerType)
			}
			if chat == request {
				continue
			}
			forward = chat
		case *core.ResponsesRequest:
			responses := adaptResponsesExtraContent(request, providerType)
			if responses == request {
				continue
			}
			forward = responses
		default:
			if anthropicDialect {
				return nil, core.NewInvalidRequestError(
					fmt.Sprintf("requests[%d]: Anthropic Message Batch item is not a chat completion", i), nil,
				)
			}
			continue
		}
		body, err := json.Marshal(forward)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("requests[%d]: failed to encode adapted request", i), err)
		}
		adapted.Requests[i].Body = body
		changed = true
	}
	if !changed {
		return req, nil
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
