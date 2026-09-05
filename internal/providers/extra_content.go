package providers

import (
	"slices"

	"github.com/enterpilot/gomodel/internal/core"
)

// extraContentVendorFor returns the extra_content vendor a provider type owns,
// or "" for providers that carry no replay state of their own.
func extraContentVendorFor(providerType string) string {
	switch normalizedProviderType(providerType) {
	case "gemini", "vertex":
		return core.ExtraContentVendorGoogle
	case "anthropic":
		return core.ExtraContentVendorAnthropic
	}
	return ""
}

// adaptExtraContent removes the extra_content vendors the selected provider
// does not own from every message and tool call, after the route is known.
// Providers reject or misread another vendor's replay state: OpenAI refuses
// unknown members and Gemini 3 validates its signatures. The caller's request
// remains unchanged and is returned as-is when nothing has to be removed.
func adaptExtraContent(req *core.ChatRequest, providerType string) *core.ChatRequest {
	if req == nil {
		return req
	}
	vendor := extraContentVendorFor(providerType)
	if !slices.ContainsFunc(req.Messages, func(message core.Message) bool {
		return messageHasForeignExtraContent(message, vendor)
	}) {
		return req
	}
	adapted := *req
	adapted.Messages = make([]core.Message, len(req.Messages))
	for i, message := range req.Messages {
		message.ExtraFields = message.ExtraFields.WithoutForeignExtraContent(vendor)
		if message.ToolCalls != nil {
			message.ToolCalls = append([]core.ToolCall(nil), message.ToolCalls...)
			for j := range message.ToolCalls {
				call := &message.ToolCalls[j]
				call.ExtraFields = call.ExtraFields.WithoutForeignExtraContent(vendor)
				call.Function.ExtraFields = call.Function.ExtraFields.WithoutForeignExtraContent(vendor)
			}
		}
		adapted.Messages[i] = message
	}
	return &adapted
}

func messageHasForeignExtraContent(message core.Message, vendor string) bool {
	if message.ExtraFields.HasForeignExtraContent(vendor) {
		return true
	}
	return slices.ContainsFunc(message.ToolCalls, func(call core.ToolCall) bool {
		return call.ExtraFields.HasForeignExtraContent(vendor) ||
			call.Function.ExtraFields.HasForeignExtraContent(vendor)
	})
}
