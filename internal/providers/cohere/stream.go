package cohere

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

type chatStream struct {
	reader *io.PipeReader
	body   io.ReadCloser
}

func newChatStream(body io.ReadCloser, model string, includeUsage bool) io.ReadCloser {
	reader, writer := io.Pipe()
	stream := &chatStream{reader: reader, body: body}
	go convertChatStream(body, writer, model, includeUsage)
	return stream
}

func (s *chatStream) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *chatStream) Close() error {
	_ = s.reader.Close()
	return s.body.Close()
}

func convertChatStream(body io.ReadCloser, out *io.PipeWriter, model string, includeUsage bool) {
	defer func() { _ = body.Close() }()
	state := cohereStreamState{
		model:        model,
		includeUsage: includeUsage,
		created:      time.Now().Unix(),
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := state.consume(out, data.String()); err != nil {
				_ = out.CloseWithError(err)
				return
			}
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if data.Len() > 0 {
		if err := state.consume(out, data.String()); err != nil {
			_ = out.CloseWithError(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	if !state.finished {
		if err := writeStreamError(out, "Cohere stream ended before message-end"); err != nil {
			_ = out.CloseWithError(err)
			return
		}
	}
	_, _ = io.WriteString(out, "data: [DONE]\n\n")
	_ = out.Close()
}

type cohereStreamState struct {
	model        string
	includeUsage bool
	created      int64
	id           string
	roleSent     bool
	sawToolCalls bool
	finished     bool
}

func (s *cohereStreamState) consume(out io.Writer, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" || s.finished {
		return nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return core.NewProviderError("cohere", 502, "failed to parse Cohere stream event", err)
	}
	if event.ID != "" {
		s.id = event.ID
	}
	switch event.Type {
	case "message-start":
		return s.writeDelta(out, map[string]any{"role": "assistant"})
	case "content-delta":
		delta := map[string]any{}
		if event.Delta.Message.Content.Text != "" {
			delta["content"] = event.Delta.Message.Content.Text
		}
		if event.Delta.Message.Content.Thinking != "" {
			delta["reasoning_content"] = event.Delta.Message.Content.Thinking
		}
		return s.writeDelta(out, delta)
	case "tool-plan-delta":
		if event.Delta.Message.ToolPlan == "" {
			return nil
		}
		return s.writeDelta(out, map[string]any{"tool_plan": event.Delta.Message.ToolPlan})
	case "tool-call-start":
		call := event.Delta.Message.ToolCalls
		if call == nil {
			return nil
		}
		s.sawToolCalls = true
		return s.writeDelta(out, map[string]any{"tool_calls": []map[string]any{{
			"index": event.Index,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		}}})
	case "tool-call-delta":
		call := event.Delta.Message.ToolCalls
		if call == nil {
			return nil
		}
		s.sawToolCalls = true
		return s.writeDelta(out, map[string]any{"tool_calls": []map[string]any{{
			"index": event.Index,
			"function": map[string]any{
				"arguments": call.Function.Arguments,
			},
		}}})
	case "citation-start":
		citations := bytes.TrimSpace(event.Delta.Message.Citations)
		if len(citations) == 0 || bytes.Equal(citations, []byte("null")) {
			return nil
		}
		if citations[0] == '[' {
			return s.writeDelta(out, map[string]any{"citations": json.RawMessage(citations)})
		}
		return s.writeDelta(out, map[string]any{"citations": []json.RawMessage{
			core.CloneRawJSON(citations),
		}})
	case "citation-end":
		// Cohere's citation-end event is only a boundary marker; all citation
		// data is carried by the corresponding citation-start event.
		return nil
	case "message-end":
		if failure := cohereGenerationError(event.Delta.FinishReason, event.Delta.Error); failure != nil {
			s.finished = true
			return writeStreamError(out, failure.Message)
		}
		finish := normalizeFinishReason(event.Delta.FinishReason)
		if s.sawToolCalls {
			finish = "tool_calls"
		}
		return s.writeFinish(out, finish, event.Delta.Usage)
	default:
		return nil
	}
}

func (s *cohereStreamState) writeDelta(out io.Writer, delta map[string]any) error {
	if len(delta) == 0 {
		return nil
	}
	if !s.roleSent {
		if _, ok := delta["role"]; !ok {
			delta["role"] = "assistant"
		}
		s.roleSent = true
	}
	return writeStreamChunk(out, s.chunk([]map[string]any{{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}}, nil))
}

func (s *cohereStreamState) writeFinish(out io.Writer, finish string, value usage) error {
	if s.finished {
		return nil
	}
	s.finished = true
	if err := writeStreamChunk(out, s.chunk([]map[string]any{{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": finish,
	}}, nil)); err != nil {
		return err
	}
	if !s.includeUsage {
		return nil
	}
	coreUsage := toCoreUsage(value)
	usageMap := map[string]any{
		"prompt_tokens":     coreUsage.PromptTokens,
		"completion_tokens": coreUsage.CompletionTokens,
		"total_tokens":      coreUsage.TotalTokens,
	}
	if coreUsage.PromptTokensDetails != nil {
		usageMap["prompt_tokens_details"] = coreUsage.PromptTokensDetails
	}
	if len(coreUsage.RawUsage) > 0 {
		usageMap["raw_usage"] = coreUsage.RawUsage
	}
	return writeStreamChunk(out, s.chunk([]map[string]any{}, usageMap))
}

func (s *cohereStreamState) chunk(choices []map[string]any, usageValue map[string]any) map[string]any {
	if s.id == "" {
		s.id = "chatcmpl-cohere-" + strconv.FormatInt(s.created, 10)
	}
	chunk := map[string]any{
		"id":       s.id,
		"object":   "chat.completion.chunk",
		"created":  s.created,
		"model":    s.model,
		"provider": "cohere",
		"choices":  choices,
	}
	if usageValue != nil {
		chunk["usage"] = usageValue
	}
	return chunk
}

func writeStreamChunk(out io.Writer, chunk map[string]any) error {
	body, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "data: %s\n\n", body)
	return err
}

func writeStreamError(out io.Writer, message string) error {
	return writeStreamChunk(out, map[string]any{"error": map[string]any{
		"type":    core.ErrorTypeProvider,
		"message": message,
		"param":   nil,
		"code":    nil,
	}})
}
