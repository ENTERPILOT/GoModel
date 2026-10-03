package gateway

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// generationStreamObserver rebuilds a streamed response from the canonical
// SSE events and finishes the request's generation trace when the stream
// closes, so the provider-call span ends with the stream and carries its
// usage. Message content is accumulated only when the trace captures it.
type generationStreamObserver struct {
	trace     *core.GenerationTrace
	request   any
	responses bool
	content   bool
	closed    bool

	chat     core.ChatResponse
	choices  map[int]*chatChoiceState
	response *core.ResponsesResponse
}

type chatChoiceState struct {
	role         string
	text         strings.Builder
	toolCalls    map[int]*core.ToolCall
	finishReason string
}

func newChatGenerationObserver(trace *core.GenerationTrace, req *core.ChatRequest) *generationStreamObserver {
	if trace == nil {
		return nil
	}
	return &generationStreamObserver{trace: trace, request: req, content: trace.CapturesContent()}
}

func newResponsesGenerationObserver(trace *core.GenerationTrace, req *core.ResponsesRequest) *generationStreamObserver {
	if trace == nil {
		return nil
	}
	return &generationStreamObserver{trace: trace, request: req, responses: true, content: trace.CapturesContent()}
}

var (
	finishReasonKey           = []byte(`"finish_reason"`)
	responsesTerminalLiterals = [][]byte{[]byte(`"response.completed"`), []byte(`"response.incomplete"`), []byte(`"response.failed"`)}
)

// WantsJSONEvent keeps per-chunk decoding off unless content is captured: a
// chat stream is read for its usage and finish chunks, a Responses stream
// for its terminal event, which carries the whole response.
func (o *generationStreamObserver) WantsJSONEvent(raw []byte) bool {
	if o.responses {
		return slices.ContainsFunc(responsesTerminalLiterals, func(literal []byte) bool {
			return bytes.Contains(raw, literal)
		})
	}
	return o.content || usage.HasUsageObject(raw) || hasStringMember(raw, finishReasonKey)
}

// hasStringMember reports whether raw has key with a string value, so chunks
// carrying "finish_reason":null are skipped. Passthrough streams keep the
// upstream's encoding, which may put spaces around the colon.
func hasStringMember(raw, key []byte) bool {
	for rest := raw; ; {
		i := bytes.Index(rest, key)
		if i < 0 {
			return false
		}
		rest = bytes.TrimLeft(rest[i+len(key):], " \t\r\n")
		if len(rest) > 0 && rest[0] == ':' {
			if value := bytes.TrimLeft(rest[1:], " \t\r\n"); len(value) > 0 && value[0] == '"' {
				return true
			}
		}
	}
}

func (o *generationStreamObserver) OnJSONEvent(event map[string]any) {
	if o.responses {
		o.observeResponsesEvent(event)
		return
	}
	o.observeChatChunk(event)
}

func (o *generationStreamObserver) OnStreamClose() {
	if o.closed {
		return
	}
	o.closed = true
	outcome := core.GenerationOutcome{Request: o.request}
	if o.responses {
		if o.response != nil {
			outcome.Response = o.response
		}
	} else if o.chat.ID != "" || o.chat.Model != "" || len(o.choices) > 0 {
		outcome.Response = o.chatResponse()
	}
	o.trace.Finish(outcome)
}

func (o *generationStreamObserver) observeResponsesEvent(event map[string]any) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed":
	default:
		return
	}
	encoded, err := json.Marshal(event["response"])
	if err != nil {
		return
	}
	var resp core.ResponsesResponse
	if json.Unmarshal(encoded, &resp) == nil {
		o.response = &resp
	}
}

func (o *generationStreamObserver) observeChatChunk(event map[string]any) {
	if id, ok := event["id"].(string); ok && id != "" {
		o.chat.ID = id
	}
	if model, ok := event["model"].(string); ok && model != "" {
		o.chat.Model = model
	}
	if raw, ok := event["usage"].(map[string]any); ok {
		o.chat.Usage.PromptTokens = intValue(raw["prompt_tokens"])
		o.chat.Usage.CompletionTokens = intValue(raw["completion_tokens"])
		o.chat.Usage.TotalTokens = intValue(raw["total_tokens"])
	}
	choices, _ := event["choices"].([]any)
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		state := o.choice(intValue(choice["index"]))
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
			state.finishReason = reason
		}
		if delta, ok := choice["delta"].(map[string]any); ok && o.content {
			state.observeDelta(delta)
		}
	}
}

func (o *generationStreamObserver) choice(index int) *chatChoiceState {
	if o.choices == nil {
		o.choices = make(map[int]*chatChoiceState)
	}
	state, ok := o.choices[index]
	if !ok {
		state = &chatChoiceState{}
		o.choices[index] = state
	}
	return state
}

func (s *chatChoiceState) observeDelta(delta map[string]any) {
	if role, ok := delta["role"].(string); ok && role != "" {
		s.role = role
	}
	if text, ok := delta["content"].(string); ok {
		s.text.WriteString(text)
	}
	calls, _ := delta["tool_calls"].([]any)
	for _, raw := range calls {
		call, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index := intValue(call["index"])
		if s.toolCalls == nil {
			s.toolCalls = make(map[int]*core.ToolCall)
		}
		state, ok := s.toolCalls[index]
		if !ok {
			state = &core.ToolCall{Type: "function"}
			s.toolCalls[index] = state
		}
		if id, ok := call["id"].(string); ok && id != "" {
			state.ID = id
		}
		if function, ok := call["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && name != "" {
				state.Function.Name = name
			}
			if arguments, ok := function["arguments"].(string); ok {
				state.Function.Arguments += arguments
			}
		}
	}
}

func (o *generationStreamObserver) chatResponse() *core.ChatResponse {
	resp := o.chat
	for _, index := range sortedKeys(o.choices) {
		state := o.choices[index]
		message := core.ResponseMessage{Role: state.role}
		if text := state.text.String(); text != "" {
			message.Content = text
		}
		for _, callIndex := range sortedKeys(state.toolCalls) {
			message.ToolCalls = append(message.ToolCalls, *state.toolCalls[callIndex])
		}
		resp.Choices = append(resp.Choices, core.Choice{Index: index, Message: message, FinishReason: state.finishReason})
	}
	return &resp
}

func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// intValue reads a JSON number decoded into an interface.
func intValue(value any) int {
	number, _ := value.(float64)
	return int(number)
}
