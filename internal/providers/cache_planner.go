package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const cachePointField = "_gomodel_cache_point"

type cachePlanner struct{}

func newCachePlanner() *cachePlanner { return &cachePlanner{} }

func (p *cachePlanner) planChat(req *core.ChatRequest, providerType string, selector core.ModelSelector) *core.ChatRequest {
	if req == nil || len(req.Messages) < 2 || hasCacheDirective(req.ExtraFields) {
		return req
	}
	prefixBody, err := json.Marshal(struct {
		Tools    []map[string]any `json:"tools,omitempty"`
		Messages []core.Message   `json:"messages"`
	}{req.Tools, req.Messages[:len(req.Messages)-1]})
	if err != nil || estimatedTokens(prefixBody) < providerCacheMinimum(providerType, selector.Model) {
		return req
	}

	planned, ok := cloneChatRequest(req)
	if !ok {
		return req
	}
	key := cacheAffinityKey(providerType, selector, req.User, prefixBody)
	switch normalizedProviderType(providerType) {
	case "openai":
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"prompt_cache_key": jsonString(key),
		})
		if supportsExplicitOpenAICache(selector.Model) {
			if markOpenAIChatBreakpoint(&planned.Messages) {
				planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
					"prompt_cache_options": json.RawMessage(`{"mode":"explicit"}`),
				})
			}
		}
	case "anthropic":
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
		})
	case "bedrock":
		markLastChatPrefix(&planned.Messages, cachePointField, json.RawMessage(`true`))
	case "gemini", "vertex":
		planned.PromptCachePlan = &core.PromptCachePlan{Key: key}
	}
	return planned
}

func (p *cachePlanner) planResponses(req *core.ResponsesRequest, providerType string, selector core.ModelSelector) *core.ResponsesRequest {
	if req == nil {
		return req
	}
	items, ok := req.Input.([]core.ResponsesInputElement)
	if !ok || len(items) < 2 || hasCacheDirective(req.ExtraFields) {
		return req
	}
	prefixBody, err := json.Marshal(struct {
		Instructions string                       `json:"instructions,omitempty"`
		Tools        []map[string]any             `json:"tools,omitempty"`
		Input        []core.ResponsesInputElement `json:"input"`
	}{req.Instructions, req.Tools, items[:len(items)-1]})
	if err != nil || estimatedTokens(prefixBody) < providerCacheMinimum(providerType, selector.Model) {
		return req
	}
	planned, ok := cloneResponsesRequest(req)
	if !ok {
		return req
	}
	key := cacheAffinityKey(providerType, selector, req.User, prefixBody)
	switch normalizedProviderType(providerType) {
	case "openai":
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"prompt_cache_key": jsonString(key),
		})
		if supportsExplicitOpenAICache(selector.Model) && markOpenAIResponsesBreakpoint(planned) {
			planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
				"prompt_cache_options": json.RawMessage(`{"mode":"explicit"}`),
			})
		}
	case "anthropic":
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
		})
	}
	return planned
}

func cloneChatRequest(req *core.ChatRequest) (*core.ChatRequest, bool) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false
	}
	var clone core.ChatRequest
	if err := json.Unmarshal(body, &clone); err != nil {
		return nil, false
	}
	return &clone, true
}

func cloneResponsesRequest(req *core.ResponsesRequest) (*core.ResponsesRequest, bool) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false
	}
	var clone core.ResponsesRequest
	if err := json.Unmarshal(body, &clone); err != nil {
		return nil, false
	}
	return &clone, true
}

func hasCacheDirective(fields core.UnknownJSONFields) bool {
	for _, key := range []string{"cache_control", "cached_content", "prompt_cache_key", "prompt_cache_options"} {
		if len(fields.Lookup(key)) > 0 {
			return true
		}
	}
	return false
}

func markLastChatPrefix(messages *[]core.Message, field string, value json.RawMessage) {
	for i := len(*messages) - 2; i >= 0; i-- {
		msg := &(*messages)[i]
		if strings.TrimSpace(core.ExtractTextContent(msg.Content)) == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		msg.ExtraFields = mergeCacheExtras(msg.ExtraFields, map[string]json.RawMessage{field: value})
		return
	}
}

func markOpenAIChatBreakpoint(messages *[]core.Message) bool {
	for i := len(*messages) - 2; i >= 0; i-- {
		msg := &(*messages)[i]
		switch content := msg.Content.(type) {
		case string:
			if content == "" {
				continue
			}
			msg.Content = []core.ContentPart{{
				Type: "text", Text: content,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
				}),
			}}
			return true
		case []core.ContentPart:
			for j := len(content) - 1; j >= 0; j-- {
				if content[j].Type == "text" || content[j].Type == "image_url" || content[j].Type == "input_audio" {
					content[j].ExtraFields = mergeCacheExtras(content[j].ExtraFields, map[string]json.RawMessage{
						"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
					})
					msg.Content = content
					return true
				}
			}
		}
	}
	return false
}

func markOpenAIResponsesBreakpoint(req *core.ResponsesRequest) bool {
	items, ok := req.Input.([]core.ResponsesInputElement)
	if !ok {
		return false
	}
	for i := len(items) - 2; i >= 0; i-- {
		switch content := items[i].Content.(type) {
		case string:
			if content == "" {
				continue
			}
			items[i].Content = []core.ContentPart{{
				Type: "input_text", Text: content,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
				}),
			}}
			req.Input = items
			return true
		case []core.ContentPart:
			for j := len(content) - 1; j >= 0; j-- {
				if content[j].Type == "input_text" || content[j].Type == "input_image" || content[j].Type == "input_file" {
					content[j].ExtraFields = mergeCacheExtras(content[j].ExtraFields, map[string]json.RawMessage{
						"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
					})
					items[i].Content = content
					req.Input = items
					return true
				}
			}
		case []any:
			for j := len(content) - 1; j >= 0; j-- {
				block, ok := content[j].(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := block["type"].(string)
				if blockType == "input_text" || blockType == "input_image" || blockType == "input_file" {
					if _, exists := block["prompt_cache_breakpoint"]; !exists {
						block["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
					}
					items[i].Content = content
					req.Input = items
					return true
				}
			}
		}
	}
	return false
}

func mergeCacheExtras(base core.UnknownJSONFields, values map[string]json.RawMessage) core.UnknownJSONFields {
	additions := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		if len(base.Lookup(key)) == 0 {
			additions[key] = value
		}
	}
	merged, err := core.MergeUnknownJSONFields(base, additions)
	if err != nil {
		return base
	}
	return merged
}

func cacheAffinityKey(providerType string, selector core.ModelSelector, user string, prefix []byte) string {
	hash := sha256.New()
	hash.Write([]byte(normalizedProviderType(providerType)))
	hash.Write([]byte{0})
	hash.Write([]byte(selector.Provider))
	hash.Write([]byte{0})
	hash.Write([]byte(selector.Model))
	hash.Write([]byte{0})
	hash.Write([]byte(user))
	hash.Write([]byte{0})
	hash.Write(prefix)
	return "gomodel-" + hex.EncodeToString(hash.Sum(nil)[:16])
}

func estimatedTokens(body []byte) int { return (len(body) + 3) / 4 }

func providerCacheMinimum(providerType, model string) int {
	providerType = normalizedProviderType(providerType)
	model = strings.ToLower(model)
	switch providerType {
	case "openai":
		return 1024
	case "anthropic":
		if strings.Contains(model, "haiku-3") && !strings.Contains(model, "3-5") && !strings.Contains(model, "3.5") {
			return 4096
		}
		if strings.Contains(model, "haiku") {
			return 2048
		}
		return 1024
	case "gemini", "vertex":
		return 4096
	case "bedrock":
		return 1024
	default:
		return int(^uint(0) >> 1)
	}
}

func normalizedProviderType(providerType string) string {
	return strings.ToLower(strings.TrimSpace(providerType))
}

func supportsExplicitOpenAICache(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "gpt-5.6")
}

func jsonString(value string) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}
