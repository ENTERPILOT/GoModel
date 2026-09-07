package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"log/slog"
	"maps"
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
	prefix := req.Messages[:len(req.Messages)-1]
	if hasChatCacheDirective(req, prefix) {
		return req
	}
	digest := newPrefixDigest(providerType, selector, req.User)
	digest.writeChatPrefix(req.Tools, prefix)
	if digest.err != nil || digest.tokens() < minimum {
		return req
	}

	planned := cloneChatRequest(req)
	key := digest.key()
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
	digest := newPrefixDigest(providerType, selector, req.User)
	digest.writeResponsesPrefix(req.Instructions, req.Tools, items[:len(items)-1])
	if digest.err != nil || digest.tokens() < providerCacheMinimum(profile, selector.Model) {
		return req
	}
	planned := cloneResponsesRequest(req, items)
	key := digest.key()
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

// cloneChatRequest returns a copy the planner may annotate without touching
// the caller's request. Only what the planner writes is duplicated: the
// request struct, the Messages slice header, and the internal cache plan.
// ExtraFields values are immutable raw JSON, and the mark* helpers copy any
// nested content slice or map before writing into it.
func cloneChatRequest(req *core.ChatRequest) *core.ChatRequest {
	clone := *req
	clone.Messages = slices.Clone(req.Messages)
	if req.PromptCachePlan != nil {
		plan := *req.PromptCachePlan
		clone.PromptCachePlan = &plan
	}
	return &clone
}

// cloneResponsesRequest mirrors cloneChatRequest for the Responses API. items
// is req.Input already asserted to its typed slice form; the clone owns a
// fresh slice header so per-item writes never reach the caller.
func cloneResponsesRequest(req *core.ResponsesRequest, items []core.ResponsesInputElement) *core.ResponsesRequest {
	clone := *req
	clone.Input = slices.Clone(items)
	return &clone
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
				if content[j].Type == "text" || content[j].Type == "image_url" || content[j].Type == "input_audio" || content[j].Type == "file" {
					parts := slices.Clone(content)
					parts[j].ExtraFields = mergeCacheExtras(parts[j].ExtraFields, map[string]json.RawMessage{
						"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
					})
					msg.Content = parts
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
		var blocks []any
		switch content := items[i].Content.(type) {
		case string:
			if content == "" {
				continue
			}
			blocks = []any{map[string]any{"type": "input_text", "text": content}}
		case []core.ContentPart:
			blocks = core.ResponsesBlocksFromContentParts(content)
		case []any:
			blocks = content
		case []map[string]any:
			blocks = make([]any, len(content))
			for j, block := range content {
				blocks[j] = block
			}
		default:
			continue
		}
		marked, ok := markResponsesBlockBreakpoint(blocks)
		if !ok {
			continue
		}
		items[i].Content = marked
		req.Input = items
		return true
	}
	return false
}

// markResponsesBlockBreakpoint attaches the explicit breakpoint to the last
// cacheable block and returns the updated slice, or false when no block
// qualifies. Blocks stay generic maps so the Responses content vocabulary
// (input_text, input_image, ...) reaches the provider verbatim.
func markResponsesBlockBreakpoint(blocks []any) ([]any, bool) {
	for j, c := range slices.Backward(blocks) {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		if blockType != "text" && blockType != "input_text" && blockType != "input_image" && blockType != "input_file" {
			continue
		}
		if _, exists := block["prompt_cache_breakpoint"]; exists {
			return blocks, true
		}
		marked := maps.Clone(block)
		marked["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
		updated := slices.Clone(blocks)
		updated[j] = marked
		return updated, true
	}
	return nil, false
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

// prefixDigest hashes the cache-relevant request prefix as it is encoded, so
// the affinity key and the token estimate derive from the same bytes without
// materializing the serialized prefix. The bytes fed to the hash are exactly
// the JSON of the prefix object the planner used to marshal in full, which
// keeps affinity keys stable across gateway versions.
type prefixDigest struct {
	hash hash.Hash
	enc  *json.Encoder
	// bytes counts prefix bytes only; the key header is excluded.
	bytes int
	err   error
	// encoding is set while the encoder writes one value so the newline it
	// appends after every value can be dropped instead of hashed.
	encoding bool
}

func newPrefixDigest(providerType string, selector core.ModelSelector, user string) *prefixDigest {
	d := &prefixDigest{hash: sha256.New()}
	d.enc = json.NewEncoder(d)
	d.hash.Write([]byte(normalizedProviderType(providerType)))
	d.hash.Write([]byte{0})
	d.hash.Write([]byte(selector.Provider))
	d.hash.Write([]byte{0})
	d.hash.Write([]byte(selector.Model))
	d.hash.Write([]byte{0})
	d.hash.Write([]byte(user))
	d.hash.Write([]byte{0})
	return d
}

// Write implements io.Writer for the JSON encoder. The newline the encoder
// emits after each value is not part of the prefix and is dropped.
func (d *prefixDigest) Write(p []byte) (int, error) {
	n := len(p)
	if d.encoding && n > 0 && p[n-1] == '\n' {
		p = p[:n-1]
	}
	d.hash.Write(p)
	d.bytes += len(p)
	return n, nil
}

func (d *prefixDigest) literal(s string) {
	if d.err == nil {
		_, _ = d.Write([]byte(s))
	}
}

func (d *prefixDigest) encode(value any) {
	if d.err != nil {
		return
	}
	d.encoding = true
	d.err = d.enc.Encode(value)
	d.encoding = false
}

// writeChatPrefix streams {"tools":[...],"messages":[...]} into the digest.
func (d *prefixDigest) writeChatPrefix(tools []map[string]any, messages []core.Message) {
	d.literal("{")
	if len(tools) > 0 {
		d.literal(`"tools":`)
		d.encode(tools)
		d.literal(",")
	}
	d.literal(`"messages":[`)
	for i := range messages {
		if i > 0 {
			d.literal(",")
		}
		d.encode(messages[i])
	}
	d.literal("]}")
}

// writeResponsesPrefix streams {"instructions":"...","tools":[...],"input":[...]}
// into the digest.
func (d *prefixDigest) writeResponsesPrefix(instructions string, tools []map[string]any, items []core.ResponsesInputElement) {
	d.literal("{")
	if instructions != "" {
		d.literal(`"instructions":`)
		d.encode(instructions)
		d.literal(",")
	}
	if len(tools) > 0 {
		d.literal(`"tools":`)
		d.encode(tools)
		d.literal(",")
	}
	d.literal(`"input":[`)
	for i := range items {
		if i > 0 {
			d.literal(",")
		}
		d.encode(items[i])
	}
	d.literal("]}")
}

func (d *prefixDigest) tokens() int { return (d.bytes + 3) / 4 }

func (d *prefixDigest) key() string {
	return "gomodel-" + hex.EncodeToString(d.hash.Sum(nil)[:16])
}

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
