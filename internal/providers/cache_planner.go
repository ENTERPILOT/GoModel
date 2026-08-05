package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	providerPromptCachePlannerEnabledEnv = "PROVIDER_PROMPT_CACHE_PLANNER_ENABLED"
)

type promptCacheMode uint8

const (
	promptCacheUnsupported promptCacheMode = iota
	promptCacheOpenAI
	promptCacheAnthropic
	promptCacheBedrock
	promptCacheGemini
)

type promptCacheProfile struct {
	mode                         promptCacheMode
	acceptsAnthropicCacheControl bool
}

type cachePlanner struct {
	enabled bool
}

func newCachePlanner() *cachePlanner {
	raw, configured := os.LookupEnv(providerPromptCachePlannerEnabledEnv)
	if !configured || strings.TrimSpace(raw) == "" {
		return &cachePlanner{enabled: true}
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("invalid provider prompt-cache planner flag; using default",
			"env", providerPromptCachePlannerEnabledEnv,
			"value", raw,
			"default", true)
		return &cachePlanner{enabled: true}
	}
	return &cachePlanner{enabled: enabled}
}

func (p *cachePlanner) planChat(req *core.ChatRequest, providerType string, selector core.ModelSelector) *core.ChatRequest {
	profile := promptCacheProfileFor(providerType)
	if p == nil || !p.enabled || req == nil || len(req.Messages) < 2 || profile.mode == promptCacheUnsupported {
		return req
	}
	minimum := providerCacheMinimum(profile, selector.Model)
	if tokens, conclusive := estimateSimpleChatPrefixTokens(req); conclusive && tokens < minimum {
		return req
	}
	prefixMessages := req.Messages[:len(req.Messages)-1]
	if hasChatCacheDirective(req, prefixMessages) {
		return req
	}
	prefixBody, err := json.Marshal(struct {
		Tools    []map[string]any `json:"tools,omitempty"`
		Messages []core.Message   `json:"messages"`
	}{req.Tools, prefixMessages})
	if err != nil || estimatedTokens(prefixBody) < minimum {
		return req
	}

	planned, ok := cloneChatRequest(req)
	if !ok {
		return req
	}
	key := cacheAffinityKey(providerType, selector, req.User, prefixBody)
	switch profile.mode {
	case promptCacheOpenAI:
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
	case promptCacheAnthropic:
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
		})
	case promptCacheBedrock:
		markLastChatPrefix(&planned.Messages, core.GatewayCachePointField, json.RawMessage(`true`))
	case promptCacheGemini:
		planned.PromptCachePlan = &core.PromptCachePlan{Key: key}
	}
	return planned
}

func (p *cachePlanner) planResponses(req *core.ResponsesRequest, providerType string, selector core.ModelSelector) *core.ResponsesRequest {
	profile := promptCacheProfileFor(providerType)
	if p == nil || !p.enabled || req == nil {
		return req
	}
	// Only OpenAI and Anthropic Responses requests have cache directives that
	// this planner can emit. Reject other modes before scanning or cloning.
	if profile.mode != promptCacheOpenAI && profile.mode != promptCacheAnthropic {
		return req
	}
	items, ok := req.Input.([]core.ResponsesInputElement)
	if !ok || len(items) < 2 || hasResponsesCacheDirective(req, items[:len(items)-1]) {
		return req
	}
	prefixBody, err := json.Marshal(struct {
		Instructions string                       `json:"instructions,omitempty"`
		Tools        []map[string]any             `json:"tools,omitempty"`
		Input        []core.ResponsesInputElement `json:"input"`
	}{req.Instructions, req.Tools, items[:len(items)-1]})
	if err != nil || estimatedTokens(prefixBody) < providerCacheMinimum(profile, selector.Model) {
		return req
	}
	planned, ok := cloneResponsesRequest(req)
	if !ok {
		return req
	}
	key := cacheAffinityKey(providerType, selector, req.User, prefixBody)
	switch profile.mode {
	case promptCacheOpenAI:
		planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
			"prompt_cache_key": jsonString(key),
		})
		if supportsExplicitOpenAICache(selector.Model) && markOpenAIResponsesBreakpoint(planned) {
			planned.ExtraFields = mergeCacheExtras(planned.ExtraFields, map[string]json.RawMessage{
				"prompt_cache_options": json.RawMessage(`{"mode":"explicit"}`),
			})
		}
	case promptCacheAnthropic:
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
	if req.PromptCachePlan != nil {
		plan := *req.PromptCachePlan
		clone.PromptCachePlan = &plan
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

var cacheDirectiveKeys = []string{
	"cache_control",
	"cached_content",
	"prompt_cache_key",
	"prompt_cache_options",
	"prompt_cache_breakpoint",
	core.GatewayCachePointField,
}

func hasCacheDirective(fields core.UnknownJSONFields) bool {
	for _, key := range cacheDirectiveKeys {
		if len(fields.Lookup(key)) > 0 {
			return true
		}
	}
	return false
}

func hasChatCacheDirective(req *core.ChatRequest, prefix []core.Message) bool {
	if hasCacheDirective(req.ExtraFields) || anyHasCacheDirective(req.Tools) {
		return true
	}
	for _, message := range prefix {
		if hasCacheDirective(message.ExtraFields) {
			return true
		}
		for _, call := range message.ToolCalls {
			if hasCacheDirective(call.ExtraFields) || hasCacheDirective(call.Function.ExtraFields) {
				return true
			}
		}
		if contentHasCacheDirective(message.Content) {
			return true
		}
	}
	return false
}

func hasResponsesCacheDirective(req *core.ResponsesRequest, prefix []core.ResponsesInputElement) bool {
	if hasCacheDirective(req.ExtraFields) || anyHasCacheDirective(req.Tools) {
		return true
	}
	for _, item := range prefix {
		if hasCacheDirective(item.ExtraFields) || contentHasCacheDirective(item.Content) {
			return true
		}
	}
	return false
}

func contentHasCacheDirective(content any) bool {
	switch typed := content.(type) {
	case []core.ContentPart:
		for _, part := range typed {
			if hasCacheDirective(part.ExtraFields) ||
				(part.ImageURL != nil && hasCacheDirective(part.ImageURL.ExtraFields)) ||
				(part.InputAudio != nil && hasCacheDirective(part.InputAudio.ExtraFields)) {
				return true
			}
		}
		return false
	default:
		return anyHasCacheDirective(typed)
	}
}

func anyHasCacheDirective(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isCacheDirectiveKey(key) || anyHasCacheDirective(child) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(typed, anyHasCacheDirective) {
			return true
		}
	case []map[string]any:
		for _, child := range typed {
			if anyHasCacheDirective(child) {
				return true
			}
		}
	}
	return false
}

func isCacheDirectiveKey(key string) bool {
	return slices.Contains(cacheDirectiveKeys, key)
}

func estimateSimpleChatPrefixTokens(req *core.ChatRequest) (int, bool) {
	if len(req.Tools) > 0 {
		return 0, false
	}
	bytes := 0
	for _, message := range req.Messages[:len(req.Messages)-1] {
		bytes += len(message.Role) + len(message.ToolCallID) + 16
		switch content := message.Content.(type) {
		case string:
			bytes += len(content)
		case []core.ContentPart:
			for _, part := range content {
				if part.Type != "text" && part.Type != "input_text" {
					return 0, false
				}
				bytes += len(part.Text) + 16
			}
		default:
			return 0, false
		}
		if len(message.ToolCalls) > 0 {
			return 0, false
		}
	}
	return (bytes + 3) / 4, true
}

func markLastChatPrefix(messages *[]core.Message, field string, value json.RawMessage) {
	for i := len(*messages) - 2; i >= 0; i-- {
		msg := &(*messages)[i]
		switch msg.Role {
		case "system", "developer", "user", "assistant":
		default:
			continue
		}
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
				if content[j].Type == "text" || content[j].Type == "input_text" || content[j].Type == "input_image" || content[j].Type == "input_file" {
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
				if blockType == "text" || blockType == "input_text" || blockType == "input_image" || blockType == "input_file" {
					if _, exists := block["prompt_cache_breakpoint"]; !exists {
						block["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
					}
					items[i].Content = content
					req.Input = items
					return true
				}
			}
		case []map[string]any:
			for j := len(content) - 1; j >= 0; j-- {
				blockType, _ := content[j]["type"].(string)
				if blockType == "text" || blockType == "input_text" || blockType == "input_image" || blockType == "input_file" {
					if _, exists := content[j]["prompt_cache_breakpoint"]; !exists {
						content[j]["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
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

func providerCacheMinimum(profile promptCacheProfile, model string) int {
	model = strings.NewReplacer(".", "-", "_", "-").Replace(strings.ToLower(model))
	switch profile.mode {
	case promptCacheOpenAI:
		return 1024
	case promptCacheAnthropic:
		if strings.Contains(model, "fable-5") || strings.Contains(model, "mythos-5") {
			return 512
		}
		if strings.Contains(model, "mythos-preview") || strings.Contains(model, "opus-4-7") {
			return 2048
		}
		if strings.Contains(model, "haiku-4-5") || strings.Contains(model, "opus-4-5") || strings.Contains(model, "opus-4-6") {
			return 4096
		}
		if strings.Contains(model, "haiku") {
			return 2048
		}
		return 1024
	case promptCacheGemini:
		return 4096
	case promptCacheBedrock:
		if strings.Contains(model, "nova") {
			return 1536
		}
		if strings.Contains(model, "claude") {
			// AWS documents a Bedrock-specific 4096-token checkpoint minimum
			// for these models, including Sonnet 4.5; the direct Anthropic API
			// minimum can differ for the same model family.
			if strings.Contains(model, "haiku-4-5") || strings.Contains(model, "sonnet-4-5") ||
				strings.Contains(model, "opus-4-5") || strings.Contains(model, "opus-4-6") {
				return 4096
			}
			return 1024
		}
		return int(^uint(0) >> 1)
	default:
		return int(^uint(0) >> 1)
	}
}

func promptCacheProfileFor(providerType string) promptCacheProfile {
	switch normalizedProviderType(providerType) {
	case "openai":
		return promptCacheProfile{mode: promptCacheOpenAI}
	case "anthropic":
		return promptCacheProfile{mode: promptCacheAnthropic, acceptsAnthropicCacheControl: true}
	case "openrouter":
		return promptCacheProfile{acceptsAnthropicCacheControl: true}
	case "bedrock":
		return promptCacheProfile{mode: promptCacheBedrock}
	case "bedrock-mantle":
		// Mantle exposes Bedrock models through the OpenAI-compatible cache
		// fields, not Converse cachePoint blocks.
		return promptCacheProfile{mode: promptCacheOpenAI}
	case "gemini":
		return promptCacheProfile{mode: promptCacheGemini}
	default:
		return promptCacheProfile{}
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
