package providers

import (
	"maps"
	"slices"

	"github.com/goccy/go-json"

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

// adaptResponsesExtraContent is adaptExtraContent for a Responses request:
// the foreign vendors are removed from every input item's extra_content
// before the provider (native or chat-backed) sees the items. The caller's
// request and its items remain unchanged.
func adaptResponsesExtraContent(req *core.ResponsesRequest, providerType string) *core.ResponsesRequest {
	if req == nil {
		return req
	}
	vendor := extraContentVendorFor(providerType)
	var items []any
	switch in := req.Input.(type) {
	case []any:
		items = in
	case []map[string]any:
		items = make([]any, len(in))
		for i, item := range in {
			items[i] = item
		}
	case []core.ResponsesInputElement:
		if !slices.ContainsFunc(in, func(item core.ResponsesInputElement) bool {
			return item.ExtraFields.HasForeignExtraContent(vendor)
		}) {
			return req
		}
		adapted := *req
		elements := append([]core.ResponsesInputElement(nil), in...)
		for i := range elements {
			elements[i].ExtraFields = elements[i].ExtraFields.WithoutForeignExtraContent(vendor)
		}
		adapted.Input = elements
		return &adapted
	default:
		return req
	}

	changed := false
	adaptedItems := make([]any, len(items))
	for i, item := range items {
		adaptedItems[i] = item
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := object[core.ExtraContentField]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		fields := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{core.ExtraContentField: encoded})
		if !fields.HasForeignExtraContent(vendor) {
			continue
		}
		cloned := make(map[string]any, len(object))
		maps.Copy(cloned, object)
		if kept := core.KeepExtraContentVendor(encoded, vendor); kept != nil {
			cloned[core.ExtraContentField] = kept
		} else {
			delete(cloned, core.ExtraContentField)
		}
		adaptedItems[i] = cloned
		changed = true
	}
	if !changed {
		return req
	}
	adapted := *req
	adapted.Input = adaptedItems
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
