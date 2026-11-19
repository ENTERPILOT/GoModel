package geminiapi

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResponseOptions carries what the request asked of the response.
type ResponseOptions struct {
	// IncludeThoughts returns the model's reasoning as thought parts, as
	// thinkingConfig.includeThoughts does on the Gemini API. Without it
	// reasoning is left out, which is Gemini's default.
	IncludeThoughts bool
}

// FromChatResponse renders a canonical chat response in the generateContent
// response shape.
func FromChatResponse(resp *core.ChatResponse, opts ResponseOptions) *GenerateContentResponse {
	out := &GenerateContentResponse{Candidates: []Candidate{}}
	if resp == nil {
		return out
	}
	out.ModelVersion = resp.Model
	out.ResponseID = resp.ID
	out.UsageMetadata = usageFromCore(resp.Usage)

	for _, choice := range resp.Choices {
		var parts []ResponsePart
		if opts.IncludeThoughts {
			if thinking := reasoningContent(choice.Message.ExtraFields); thinking != "" {
				parts = append(parts, ResponsePart{Text: &thinking, Thought: true})
			}
		}
		signature := thoughtSignatureOf(choice.Message.ExtraFields)
		if text := core.ExtractTextContent(choice.Message.Content); text != "" || (signature != "" && len(choice.Message.ToolCalls) == 0) {
			parts = append(parts, ResponsePart{Text: &text, ThoughtSignature: signature})
		}
		for _, call := range choice.Message.ToolCalls {
			parts = append(parts, functionCallPart(call.ID, call.Function.Name, call.Function.Arguments, thoughtSignatureOf(call.ExtraFields)))
		}
		out.Candidates = append(out.Candidates, Candidate{
			Content:      ResponseContent{Role: "model", Parts: orEmptyText(parts)},
			FinishReason: finishReasonFromChat(choice.FinishReason),
			Index:        choice.Index,
		})
	}
	return out
}

// orEmptyText gives a candidate without output an empty text part: Gemini
// SDKs read the answer from the first part, and a turn that produced nothing
// (a length stop before any text) is an empty answer, not a missing one.
func orEmptyText(parts []ResponsePart) []ResponsePart {
	if len(parts) > 0 {
		return parts
	}
	empty := ""
	return []ResponsePart{{Text: &empty}}
}

func functionCallPart(id, name, arguments, signature string) ResponsePart {
	return ResponsePart{
		FunctionCall:     &FunctionCall{ID: id, Name: name, Args: argumentsToRaw(arguments)},
		ThoughtSignature: signature,
	}
}

// finishReasonFromChat maps an OpenAI finish_reason onto Gemini's
// finishReason. Gemini reports a turn that ends in function calls as STOP.
func finishReasonFromChat(finish string) string {
	switch finish {
	case "", "stop", "tool_calls", "function_call":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "OTHER"
	}
}

// usageFromCore renders canonical usage in the Gemini shape.
func usageFromCore(usage core.Usage) *UsageMetadata {
	reasoning := 0
	if usage.CompletionTokensDetails != nil {
		reasoning = usage.CompletionTokensDetails.ReasoningTokens
	}
	return geminiUsage(
		usage.PromptTokens,
		usage.CompletionTokens,
		reasoning,
		intFromRaw(usage.RawUsage["cache_read_input_tokens"]),
		intFromRaw(usage.RawUsage["cache_creation_input_tokens"]),
		usage.PromptTokensDetails,
	)
}

// geminiUsage renders token counts in the Gemini shape, where
// promptTokenCount is the whole prompt including cached tokens and
// thoughtsTokenCount is reported apart from candidatesTokenCount.
// OpenAI-family providers already count that way and include reasoning in
// completion_tokens, so reasoning is taken out of the candidates. A provider
// reporting in the Anthropic shape excludes cached tokens from prompt_tokens
// and names them cache_read_input_tokens and cache_creation_input_tokens
// (nativeRead, nativeWrite), so those are added back. The response and the
// stream converter both render through here so the two agree.
func geminiUsage(promptTokens, completionTokens, reasoningTokens, nativeRead, nativeWrite int, details *core.PromptTokensDetails) *UsageMetadata {
	prompt, cached := promptTokens+nativeRead+nativeWrite, nativeRead
	if nativeRead == 0 && nativeWrite == 0 && details != nil {
		cached = details.CachedTokens
	}
	reasoningTokens = min(max(reasoningTokens, 0), completionTokens)
	return &UsageMetadata{
		PromptTokenCount:        prompt,
		CachedContentTokenCount: cached,
		CandidatesTokenCount:    completionTokens - reasoningTokens,
		ThoughtsTokenCount:      reasoningTokens,
		TotalTokenCount:         prompt + completionTokens,
	}
}

// reasoningContent extracts the reasoning text providers surface in a
// response message's extra fields: "reasoning_content" or "reasoning", in the
// precedence the streaming codec uses.
func reasoningContent(fields core.UnknownJSONFields) string {
	for _, member := range []string{"reasoning_content", "reasoning"} {
		raw := fields.Lookup(member)
		if len(raw) == 0 {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil && text != "" {
			return text
		}
	}
	return ""
}

// argumentsToRaw renders a tool-call arguments string as the JSON object
// functionCall.args holds. Anything but an object (empty, null, an array,
// invalid JSON) becomes {}: the Gemini SDKs decode args as an object only.
func argumentsToRaw(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if !strings.HasPrefix(trimmed, "{") {
		return json.RawMessage("{}")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(compact.Bytes())
}

// intFromRaw coerces a value decoded from a raw usage map into an int.
func intFromRaw(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(n)
	default:
		return 0
	}
}
