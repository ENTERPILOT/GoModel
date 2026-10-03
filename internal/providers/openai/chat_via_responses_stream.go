package openai

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// chatResponseFromResponses renders a Responses result as a chat completion.
func chatResponseFromResponses(resp *core.ResponsesResponse, provider string) (*core.ChatResponse, error) {
	if resp.Status == "failed" {
		return nil, core.NewProviderError(provider, http.StatusBadGateway, responsesErrorMessage(resp.Error), nil)
	}
	var text strings.Builder
	var calls []core.ToolCall
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text.WriteString(content.Text)
				}
			}
		case "function_call":
			calls = append(calls, core.ToolCall{
				ID:       item.CallID,
				Type:     "function",
				Function: core.FunctionCall{Name: item.Name, Arguments: item.Arguments},
			})
		}
	}
	message := core.ResponseMessage{Role: "assistant", ToolCalls: calls}
	// Chat Completions reports content null on a tool-call-only turn.
	if text.Len() > 0 || len(calls) == 0 {
		message.Content = text.String()
	}
	return &core.ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedAt,
		Model:   resp.Model,
		Choices: []core.Choice{{
			Message:      message,
			FinishReason: chatFinishReason(resp, len(calls) > 0),
		}},
		Usage: chatUsageFromResponses(resp.Usage),
	}, nil
}

func chatFinishReason(resp *core.ResponsesResponse, hasToolCalls bool) string {
	if resp.Status == "incomplete" && resp.IncompleteDetails != nil {
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		}
	}
	if hasToolCalls {
		return "tool_calls"
	}
	return "stop"
}

func chatUsageFromResponses(usage *core.ResponsesUsage) core.Usage {
	if usage == nil {
		return core.Usage{}
	}
	return core.Usage{
		PromptTokens:            usage.InputTokens,
		CompletionTokens:        usage.OutputTokens,
		TotalTokens:             usage.TotalTokens,
		PromptTokensDetails:     usage.PromptTokensDetails,
		CompletionTokensDetails: usage.CompletionTokensDetails,
		RawUsage:                usage.RawUsage,
	}
}

func responsesErrorMessage(err *core.ResponsesError) string {
	if err == nil || strings.TrimSpace(err.Message) == "" {
		return "the response failed"
	}
	return err.Message
}

// responsesEvent is the subset of a Responses stream event the chat converter
// reads.
type responsesEvent struct {
	Type     string                    `json:"type"`
	Delta    string                    `json:"delta"`
	ItemID   string                    `json:"item_id"`
	Item     *core.ResponsesOutputItem `json:"item"`
	Response *core.ResponsesResponse   `json:"response"`
	Code     string                    `json:"code"`
	Message  string                    `json:"message"`
}

// responsesChatStream converts a Responses SSE stream into a chat completion
// SSE stream. It is single-reader and must not be shared.
type responsesChatStream struct {
	reader       *bufio.Reader
	body         io.ReadCloser
	buffer       streaming.StreamBuffer
	provider     string
	model        string
	includeUsage bool

	id      string
	created int64
	// tools maps a function_call output item id to its chat tool_calls index.
	tools    map[string]int
	finished bool
	endErr   error
}

func newResponsesChatStream(body io.ReadCloser, provider, model string, includeUsage bool) io.ReadCloser {
	return &responsesChatStream{
		reader:       bufio.NewReader(body),
		body:         body,
		buffer:       streaming.NewStreamBuffer(1024),
		provider:     provider,
		model:        model,
		includeUsage: includeUsage,
		tools:        make(map[string]int),
	}
}

func (s *responsesChatStream) Read(p []byte) (int, error) {
	for {
		if s.buffer.Len() > 0 {
			return s.buffer.Read(p), nil
		}
		if s.finished {
			if s.endErr != nil {
				return 0, s.endErr
			}
			return 0, io.EOF
		}
		line, err := s.reader.ReadBytes('\n')
		if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			s.handle(bytes.TrimSpace(data))
		}
		if err != nil && !s.finished {
			// The upstream ended before a terminal event: the answer is
			// truncated, which must not look like a finished turn.
			s.finished = true
			s.endErr = streaming.IncompleteStreamError(err)
		}
	}
}

func (s *responsesChatStream) Close() error {
	s.buffer.Release()
	return s.body.Close()
}

func (s *responsesChatStream) handle(data []byte) {
	var event responsesEvent
	if s.finished || json.Unmarshal(data, &event) != nil {
		return
	}
	switch event.Type {
	case "response.created":
		if event.Response != nil {
			s.id, s.created = event.Response.ID, event.Response.CreatedAt
			if event.Response.Model != "" {
				s.model = event.Response.Model
			}
		}
		s.emit(map[string]any{"role": "assistant", "content": ""}, nil, nil)
	case "response.output_text.delta":
		s.emit(map[string]any{"content": event.Delta}, nil, nil)
	case "response.refusal.delta":
		s.emit(map[string]any{"refusal": event.Delta}, nil, nil)
	case "response.output_item.added":
		if event.Item == nil || event.Item.Type != "function_call" {
			return
		}
		index := len(s.tools)
		s.tools[event.Item.ID] = index
		s.emit(map[string]any{"tool_calls": []any{map[string]any{
			"index":    index,
			"id":       event.Item.CallID,
			"type":     "function",
			"function": map[string]any{"name": event.Item.Name, "arguments": event.Item.Arguments},
		}}}, nil, nil)
	case "response.function_call_arguments.delta":
		if index, ok := s.tools[event.ItemID]; ok {
			s.emit(map[string]any{"tool_calls": []any{map[string]any{
				"index":    index,
				"function": map[string]any{"arguments": event.Delta},
			}}}, nil, nil)
		}
	case "response.completed", "response.incomplete":
		if event.Response == nil {
			return
		}
		var usage map[string]any
		if s.includeUsage {
			usage = usagePayload(chatUsageFromResponses(event.Response.Usage))
		}
		s.emit(map[string]any{}, chatFinishReason(event.Response, len(s.tools) > 0), usage)
		s.buffer.AppendString("data: [DONE]\n\n")
		s.finished = true
	case "response.failed":
		var failure *core.ResponsesError
		if event.Response != nil {
			failure = event.Response.Error
		}
		s.fail(responsesErrorMessage(failure), "")
	case "error":
		s.fail(event.Message, event.Code)
	}
}

func (s *responsesChatStream) emit(delta map[string]any, finishReason any, usage map[string]any) {
	s.buffer.AppendString(providers.FormatChatChunkSSE(s.id, s.created, s.model, s.provider, delta, finishReason, usage))
}

// fail ends the stream with an OpenAI-style error chunk carrying the
// upstream's own message, which the OpenAI SDKs raise as an API error.
func (s *responsesChatStream) fail(message, code string) {
	if strings.TrimSpace(message) == "" {
		message = "the response failed"
	}
	payload := map[string]any{"message": message, "type": "server_error"}
	if code != "" {
		payload["code"] = code
	}
	raw, err := json.Marshal(map[string]any{"error": payload})
	if err == nil {
		s.buffer.AppendString("data: " + string(raw) + "\n\n")
	}
	s.finished = true
}

// usagePayload renders usage as the chat chunk usage object, keeping the
// prompt and completion detail members.
func usagePayload(usage core.Usage) map[string]any {
	raw, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return payload
}
