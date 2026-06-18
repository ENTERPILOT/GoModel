package responsecache

import (
	"bytes"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
)

type chatToolCallState struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

type chatChoiceState struct {
	Index        int
	Role         string
	Content      strings.Builder
	Reasoning    strings.Builder
	FinishReason string
	Logprobs     json.RawMessage
	HasLogprobs  bool
	ToolCalls    map[int]*chatToolCallState
}

type chatStreamCacheBuilder struct {
	defaults          streamResponseDefaults
	seen              bool
	ID                string
	Model             string
	Provider          string
	Object            string
	SystemFingerprint string
	Created           int64
	Usage             map[string]any
	Choices           map[int]*chatChoiceState
}

func renderCachedChatStream(requestBody, cached []byte) ([]byte, error) {
	var resp core.ChatResponse
	if err := json.Unmarshal(cached, &resp); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	includeUsage := streamIncludeUsageRequested("/v1/chat/completions", requestBody)
	usage := chatUsageMap(resp.Usage)
	if !includeUsage {
		usage = nil
	}
	for _, choice := range resp.Choices {
		delta := map[string]any{}
		role := strings.TrimSpace(choice.Message.Role)
		if role == "" {
			role = "assistant"
		}
		delta["role"] = role

		if content := core.ExtractTextContent(choice.Message.Content); content != "" {
			delta["content"] = content
		}
		if reasoning := chatReasoningContent(choice.Message); reasoning != "" {
			delta["reasoning_content"] = reasoning
		}
		if len(choice.Message.ToolCalls) > 0 {
			delta["tool_calls"] = renderChatToolCalls(choice.Message.ToolCalls)
		}

		renderedChoice := map[string]any{
			"index":         choice.Index,
			"delta":         delta,
			"finish_reason": choice.FinishReason,
		}
		if len(choice.Logprobs) > 0 {
			renderedChoice["logprobs"] = choice.Logprobs
		}

		chunk := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"model":   resp.Model,
			"choices": []map[string]any{renderedChoice},
		}
		if resp.Created != 0 {
			chunk["created"] = resp.Created
		}
		if resp.Provider != "" {
			chunk["provider"] = resp.Provider
		}
		if resp.SystemFingerprint != "" {
			chunk["system_fingerprint"] = resp.SystemFingerprint
		}
		if err := appendSSEJSONEvent(&out, "", chunk); err != nil {
			return nil, err
		}
	}

	if usage != nil {
		chunk := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"model":   resp.Model,
			"choices": []map[string]any{},
			"usage":   usage,
		}
		if resp.Created != 0 {
			chunk["created"] = resp.Created
		}
		if resp.Provider != "" {
			chunk["provider"] = resp.Provider
		}
		if err := appendSSEJSONEvent(&out, "", chunk); err != nil {
			return nil, err
		}
	}

	out.WriteString("data: [DONE]\n\n")
	return out.Bytes(), nil
}

func (b *chatStreamCacheBuilder) OnJSONEvent(event map[string]any) {
	if b == nil {
		return
	}
	b.seen = true

	if id, ok := event["id"].(string); ok && id != "" {
		b.ID = id
	}
	if model, ok := event["model"].(string); ok && model != "" {
		b.Model = model
	}
	if provider, ok := event["provider"].(string); ok && provider != "" {
		b.Provider = provider
	}
	if object, ok := event["object"].(string); ok && object != "" {
		b.Object = object
	}
	if fingerprint, ok := event["system_fingerprint"].(string); ok && fingerprint != "" {
		b.SystemFingerprint = fingerprint
	}
	if created, ok := jsonNumberToInt64(event["created"]); ok {
		b.Created = created
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		b.Usage = cloneJSONMap(usage)
	}

	choices, ok := event["choices"].([]any)
	if !ok {
		return
	}
	for _, choiceAny := range choices {
		choiceMap, ok := choiceAny.(map[string]any)
		if !ok {
			continue
		}
		index, ok := jsonNumberToInt(choiceMap["index"])
		if !ok {
			index = len(b.Choices)
		}
		state := b.choice(index)
		if finish, ok := choiceMap["finish_reason"].(string); ok && finish != "" {
			state.FinishReason = finish
		}
		if logprobs, ok := choiceMap["logprobs"]; ok {
			raw, err := json.Marshal(logprobs)
			if err == nil {
				state.Logprobs = raw
				state.HasLogprobs = true
			}
		}

		delta, ok := choiceMap["delta"].(map[string]any)
		if !ok {
			continue
		}
		if role, ok := delta["role"].(string); ok && role != "" {
			state.Role = role
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			_, _ = state.Content.WriteString(content)
		}
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			_, _ = state.Reasoning.WriteString(reasoning)
		}

		toolCalls, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, toolAny := range toolCalls {
			toolMap, ok := toolAny.(map[string]any)
			if !ok {
				continue
			}
			toolIndex, ok := jsonNumberToInt(toolMap["index"])
			if !ok {
				toolIndex = len(state.ToolCalls)
			}
			toolState := state.toolCall(toolIndex)
			if id, ok := toolMap["id"].(string); ok && id != "" {
				toolState.ID = id
			}
			if typ, ok := toolMap["type"].(string); ok && typ != "" {
				toolState.Type = typ
			}
			function, ok := toolMap["function"].(map[string]any)
			if !ok {
				continue
			}
			if name, ok := function["name"].(string); ok && name != "" {
				toolState.Name = name
			}
			if arguments, ok := function["arguments"].(string); ok && arguments != "" {
				_, _ = toolState.Arguments.WriteString(arguments)
			}
		}
	}
}

func (b *chatStreamCacheBuilder) Build() ([]byte, bool) {
	if b == nil || !b.seen {
		return nil, false
	}

	choiceIndexes := make([]int, 0, len(b.Choices))
	for index := range b.Choices {
		choiceIndexes = append(choiceIndexes, index)
	}
	sort.Ints(choiceIndexes)

	choices := make([]map[string]any, 0, len(choiceIndexes))
	for _, index := range choiceIndexes {
		state := b.Choices[index]
		message := map[string]any{
			"role": nonEmpty(state.Role, "assistant"),
		}

		content := state.Content.String()
		toolCalls := buildChatToolCalls(state.ToolCalls)
		switch {
		case content != "":
			message["content"] = content
		case len(toolCalls) > 0:
			message["content"] = nil
		default:
			message["content"] = ""
		}
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
		}
		if reasoning := state.Reasoning.String(); reasoning != "" {
			message["reasoning_content"] = reasoning
		}

		choice := map[string]any{
			"index":         index,
			"message":       message,
			"finish_reason": state.FinishReason,
		}
		if state.HasLogprobs {
			choice["logprobs"] = state.Logprobs
		}
		choices = append(choices, choice)
	}

	response := map[string]any{
		"id":      b.ID,
		"object":  "chat.completion",
		"model":   nonEmpty(b.Model, b.defaults.Model),
		"choices": choices,
	}
	if provider := nonEmpty(b.Provider, b.defaults.Provider); provider != "" {
		response["provider"] = provider
	}
	if b.Created != 0 {
		response["created"] = b.Created
	}
	if b.SystemFingerprint != "" {
		response["system_fingerprint"] = b.SystemFingerprint
	}
	if b.Usage != nil {
		response["usage"] = b.Usage
	}

	data, err := json.Marshal(response)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (b *chatStreamCacheBuilder) choice(index int) *chatChoiceState {
	state, ok := b.Choices[index]
	if ok {
		return state
	}
	state = &chatChoiceState{
		Index:     index,
		ToolCalls: make(map[int]*chatToolCallState),
	}
	b.Choices[index] = state
	return state
}

func (c *chatChoiceState) toolCall(index int) *chatToolCallState {
	state, ok := c.ToolCalls[index]
	if ok {
		return state
	}
	state = &chatToolCallState{Index: index}
	c.ToolCalls[index] = state
	return state
}

func buildChatToolCalls(states map[int]*chatToolCallState) []map[string]any {
	if len(states) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(states))
	for index := range states {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	toolCalls := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		state := states[index]
		toolCall := map[string]any{
			"id":    state.ID,
			"type":  nonEmpty(state.Type, "function"),
			"index": index,
			"function": map[string]any{
				"name":      state.Name,
				"arguments": state.Arguments.String(),
			},
		}
		toolCalls = append(toolCalls, toolCall)
	}
	return toolCalls
}

func renderChatToolCalls(toolCalls []core.ToolCall) []map[string]any {
	if len(toolCalls) == 0 {
		return nil
	}
	rendered := make([]map[string]any, 0, len(toolCalls))
	for index, toolCall := range toolCalls {
		rendered = append(rendered, map[string]any{
			"index": index,
			"id":    toolCall.ID,
			"type":  nonEmpty(toolCall.Type, "function"),
			"function": map[string]any{
				"name":      toolCall.Function.Name,
				"arguments": toolCall.Function.Arguments,
			},
		})
	}
	return rendered
}

func chatUsageMap(usage core.Usage) map[string]any {
	if usage.PromptTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.PromptTokensDetails == nil &&
		usage.CompletionTokensDetails == nil &&
		len(usage.RawUsage) == 0 {
		return nil
	}
	result, err := toJSONMap(usage)
	if err != nil {
		return nil
	}
	return result
}

func chatReasoningContent(message core.ResponseMessage) string {
	raw := message.ExtraFields.Lookup("reasoning_content")
	if len(raw) == 0 {
		return ""
	}
	var reasoning string
	if err := json.Unmarshal(raw, &reasoning); err != nil {
		return ""
	}
	return reasoning
}
