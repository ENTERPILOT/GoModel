package streaming

import (
	"maps"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// SynthesizeChatStream renders a chat completion as a chat SSE stream: per
// choice a role chunk, a reasoning chunk (when the message carries
// reasoning_content), a content chunk, one chunk per tool call and a finish
// chunk; then a usage chunk when includeUsage is set, and [DONE].
func SynthesizeChatStream(resp *core.ChatResponse, includeUsage bool) []byte {
	var out []byte
	envelope := chatTerminalChunk{
		ID:                resp.ID,
		Object:            "chat.completion.chunk",
		Created:           resp.Created,
		Model:             resp.Model,
		Provider:          resp.Provider,
		SystemFingerprint: resp.SystemFingerprint,
	}
	emit := func(choices []chatFinishChoice, usage *core.Usage) {
		chunk := envelope
		chunk.Choices = choices
		if usage != nil {
			chunk.Usage = usage
		}
		if chunk.Choices == nil {
			chunk.Choices = []chatFinishChoice{}
		}
		encoded, err := encodeJSONEvent("", chunk)
		if err == nil {
			out = append(out, encoded...)
		}
	}
	delta := func(index int, delta map[string]any) {
		emit([]chatFinishChoice{{Index: index, Delta: delta}}, nil)
	}
	for _, choice := range resp.Choices {
		role := choice.Message.Role
		if role == "" {
			role = "assistant"
		}
		delta(choice.Index, map[string]any{"role": role})
		if raw := choice.Message.ExtraFields.Lookup("reasoning_content"); len(raw) > 0 {
			delta(choice.Index, map[string]any{"reasoning_content": json.RawMessage(raw)})
		}
		if text := core.ExtractTextContent(choice.Message.Content); text != "" {
			delta(choice.Index, map[string]any{"content": text})
		}
		for pos, call := range choice.Message.ToolCalls {
			delta(choice.Index, map[string]any{"tool_calls": []map[string]any{{
				"index":    pos,
				"id":       call.ID,
				"type":     call.Type,
				"function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments},
			}}})
		}
		reason := choice.FinishReason
		if reason == "" {
			reason = "stop"
		}
		emit([]chatFinishChoice{{Index: choice.Index, Delta: map[string]any{}, FinishReason: &reason}}, nil)
	}
	if includeUsage {
		usage := resp.Usage
		emit(nil, &usage)
	}
	return append(out, doneEventBytes...)
}

// SynthesizeResponsesStream renders a Responses API response as an event
// stream: response.created, response.in_progress, then per output item the
// added/delta/done events (whole text as one output_text.delta), the
// terminal event matching resp.Status, and [DONE]. Events carry
// sequence_number.
func SynthesizeResponsesStream(resp *core.ResponsesResponse) []byte {
	var out []byte
	seq := 0
	emit := func(name string, payload map[string]any) {
		payload["type"] = name
		payload["sequence_number"] = seq
		seq++
		encoded, err := encodeJSONEvent(name, payload)
		if err == nil {
			out = append(out, encoded...)
		}
	}
	status := resp.Status
	if status == "" {
		status = "completed"
	}
	inProgress := *resp
	inProgress.Object, inProgress.Status = "response", "in_progress"
	inProgress.Output, inProgress.Usage, inProgress.Error = []core.ResponsesOutputItem{}, nil, nil
	emit("response.created", map[string]any{"response": inProgress})
	emit("response.in_progress", map[string]any{"response": inProgress})

	for index, item := range resp.Output {
		pending := item
		pending.Status = "in_progress"
		switch item.Type {
		case "message":
			pending.Content = nil
			emit("response.output_item.added", map[string]any{"output_index": index, "item": pending})
			for contentIndex, part := range item.Content {
				ref := map[string]any{"item_id": item.ID, "output_index": index, "content_index": contentIndex}
				empty := part
				empty.Text = ""
				emit("response.content_part.added", withPart(ref, "part", empty))
				if part.Type == "output_text" {
					emit("response.output_text.delta", withPart(ref, "delta", part.Text))
					emit("response.output_text.done", withPart(ref, "text", part.Text))
				}
				emit("response.content_part.done", withPart(ref, "part", part))
			}
		case "function_call":
			pending.Arguments = ""
			emit("response.output_item.added", map[string]any{"output_index": index, "item": pending})
			ref := map[string]any{"item_id": item.ID, "output_index": index}
			emit("response.function_call_arguments.delta", withPart(ref, "delta", item.Arguments))
			emit("response.function_call_arguments.done", withPart(ref, "arguments", item.Arguments))
		default:
			emit("response.output_item.added", map[string]any{"output_index": index, "item": pending})
		}
		emit("response.output_item.done", map[string]any{"output_index": index, "item": item})
	}

	final := *resp
	final.Object, final.Status = "response", status
	name := "response.completed"
	switch status {
	case "incomplete":
		name = "response.incomplete"
	case "failed":
		name = "response.failed"
	}
	emit(name, map[string]any{"response": final})
	return append(out, doneEventBytes...)
}

// withPart copies ref and adds one member.
func withPart(ref map[string]any, key string, value any) map[string]any {
	payload := make(map[string]any, len(ref)+3)
	maps.Copy(payload, ref)
	payload[key] = value
	return payload
}
