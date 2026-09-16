package streaming

import (
	"bytes"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/goccy/go-json"
)

// chatChunkView decodes the members of a chat.completion.chunk the codec
// classifies on.
type chatChunkView struct {
	ID                string           `json:"id"`
	Model             string           `json:"model"`
	Provider          string           `json:"provider"`
	SystemFingerprint string           `json:"system_fingerprint"`
	Created           int64            `json:"created"`
	Choices           []chatChoiceView `json:"choices"`
	Usage             json.RawMessage  `json:"usage"`
}

type chatChoiceView struct {
	Index        int            `json:"index"`
	Delta        *chatDeltaView `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chatDeltaView struct {
	Content          *string            `json:"content"`
	ReasoningContent json.RawMessage    `json:"reasoning_content"`
	Reasoning        json.RawMessage    `json:"reasoning"`
	ToolCalls        []chatToolCallView `json:"tool_calls"`
}

type chatToolCallView struct {
	Index    *int `json:"index"`
	Function *struct {
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// reasoningText returns the delta's reasoning text and the member carrying
// it ("reasoning_content" or "reasoning").
func (d *chatDeltaView) reasoningText() (string, string) {
	if s, ok := jsonStringOf(d.ReasoningContent); ok && s != "" {
		return s, "reasoning_content"
	}
	if s, ok := jsonStringOf(d.Reasoning); ok && s != "" {
		return s, "reasoning"
	}
	return "", ""
}

type chatCodec struct {
	id, model, provider, fingerprint string
	created                          int64
	choices                          []int
	finished                         map[int]bool
}

// ChatCodec returns a codec for OpenAI chat.completion.chunk streams.
func ChatCodec() Codec {
	return &chatCodec{finished: make(map[int]bool)}
}

func (c *chatCodec) Decode(raw RawEvent, seq int) Event {
	if raw.Comment || raw.Oversized || !jsonObject(raw.Data) {
		return decodeOther(raw, seq)
	}
	var chunk chatChunkView
	if err := json.Unmarshal(raw.Data, &chunk); err != nil {
		return decodeOther(raw, seq)
	}
	c.remember(&chunk)

	ev := Event{Seq: seq, Kind: KindOther, Name: raw.Name, Data: raw.Data}
	if len(chunk.Choices) == 0 {
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			ev.Kind = KindUsage
		}
		return ev
	}
	for _, choice := range chunk.Choices {
		ev.Choice = choice.Index
		// A delta chunk may carry the choice's finish_reason as well.
		ev.ClosesChoice = choice.FinishReason != nil
		if delta := choice.Delta; delta != nil {
			if delta.Content != nil && *delta.Content != "" {
				ev.Kind, ev.Text = KindTextDelta, *delta.Content
				return ev
			}
			if text, _ := delta.reasoningText(); text != "" {
				ev.Kind, ev.Text = KindReasoningDelta, text
				return ev
			}
			if len(delta.ToolCalls) > 0 {
				ev.Kind = KindToolCallDelta
				if idx := delta.ToolCalls[0].Index; idx != nil {
					ev.Call = *idx
				}
				if fn := delta.ToolCalls[0].Function; fn != nil {
					ev.Text = fn.Arguments
				}
				return ev
			}
		}
		ev.ClosesChoice = false
		if choice.FinishReason != nil {
			ev.Kind = KindFinish
			return ev
		}
	}
	ev.Choice = chunk.Choices[0].Index
	return ev
}

func (c *chatCodec) remember(chunk *chatChunkView) {
	if chunk.ID != "" {
		c.id = chunk.ID
	}
	if chunk.Model != "" {
		c.model = chunk.Model
	}
	if chunk.Provider != "" {
		c.provider = chunk.Provider
	}
	if chunk.SystemFingerprint != "" {
		c.fingerprint = chunk.SystemFingerprint
	}
	if chunk.Created != 0 {
		c.created = chunk.Created
	}
	for _, choice := range chunk.Choices {
		if _, seen := c.finished[choice.Index]; !seen {
			c.choices = append(c.choices, choice.Index)
			c.finished[choice.Index] = false
		}
	}
}

// Track marks the choice of an emitted finish chunk as closed, including a
// delta chunk that carries the finish_reason alongside its text.
func (c *chatCodec) Track(ev Event) {
	if ev.Kind == KindFinish || ev.ClosesChoice {
		c.finished[ev.Choice] = true
	}
}

func (c *chatCodec) RewriteText(ev Event, text string) (Event, error) {
	if !isDelta(ev.Kind) {
		return ev, ErrNotTextEvent
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &top); err != nil {
		return ev, fmt.Errorf("streaming: decode chat chunk: %w", err)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil {
		return ev, fmt.Errorf("streaming: decode chat choices: %w", err)
	}
	pos := chatChoicePosition(choices, ev.Choice)
	if pos < 0 {
		return ev, fmt.Errorf("streaming: chat chunk has no choice %d", ev.Choice)
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(choices[pos]["delta"], &delta); err != nil {
		return ev, fmt.Errorf("streaming: decode chat delta: %w", err)
	}
	if delta == nil {
		delta = make(map[string]json.RawMessage, 1)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return ev, err
	}
	switch ev.Kind {
	case KindToolCallDelta:
		if err := rewriteToolArguments(delta, encoded); err != nil {
			return ev, err
		}
	case KindReasoningDelta:
		key := "reasoning_content"
		if _, ok := delta["reasoning_content"]; !ok {
			if _, ok := delta["reasoning"]; ok {
				key = "reasoning"
			}
		}
		delta[key] = encoded
	default:
		delta["content"] = encoded
	}
	if choices[pos]["delta"], err = json.Marshal(delta); err != nil {
		return ev, err
	}
	if top["choices"], err = json.Marshal(choices); err != nil {
		return ev, err
	}
	data, err := json.Marshal(top)
	if err != nil {
		return ev, err
	}
	ev.Text = text
	ev.Data = data
	return ev, nil
}

// rewriteToolArguments sets the arguments of the delta's first tool call
// (the one Decode classified on) to the encoded JSON string.
func rewriteToolArguments(delta map[string]json.RawMessage, encoded json.RawMessage) error {
	var calls []map[string]json.RawMessage
	if err := json.Unmarshal(delta["tool_calls"], &calls); err != nil || len(calls) == 0 {
		return fmt.Errorf("streaming: chat delta carries no tool call: %w", err)
	}
	var fn map[string]json.RawMessage
	if raw, ok := calls[0]["function"]; ok && jsonNonNull(raw) {
		if err := json.Unmarshal(raw, &fn); err != nil {
			return fmt.Errorf("streaming: decode tool call function: %w", err)
		}
	}
	if fn == nil {
		fn = make(map[string]json.RawMessage, 1)
	}
	fn["arguments"] = encoded
	encodedFn, err := json.Marshal(fn)
	if err != nil {
		return err
	}
	calls[0]["function"] = encodedFn
	encodedCalls, err := json.Marshal(calls)
	if err != nil {
		return err
	}
	delta["tool_calls"] = encodedCalls
	return nil
}

// isDelta reports whether kind carries rewritable delta text.
func isDelta(kind EventKind) bool {
	return kind == KindTextDelta || kind == KindReasoningDelta || kind == KindToolCallDelta
}

// StripTerminal drops a non-null finish_reason of the event's choice and a
// non-null top-level usage.
func (c *chatCodec) StripTerminal(ev Event) (Event, bool) {
	if !isDelta(ev.Kind) {
		return ev, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &top); err != nil {
		return ev, false
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil {
		return ev, false
	}
	changed := false
	if jsonNonNull(top["usage"]) {
		delete(top, "usage")
		changed = true
	}
	if pos := chatChoicePosition(choices, ev.Choice); pos >= 0 && jsonNonNull(choices[pos]["finish_reason"]) {
		delete(choices[pos], "finish_reason")
		ev.ClosesChoice = false
		changed = true
	}
	if !changed {
		return ev, false
	}
	encoded, err := json.Marshal(choices)
	if err != nil {
		return ev, false
	}
	top["choices"] = encoded
	data, err := json.Marshal(top)
	if err != nil {
		return ev, false
	}
	ev.Data = data
	return ev, true
}

// jsonNonNull reports whether raw is a present, non-null JSON value.
func jsonNonNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

// Split turns a chunk with several choices into one chunk per choice, and
// a choice whose delta carries several tool calls, or text alongside tool
// calls, into one chunk per part, so each is decoded and transformed on
// its own. Every top-level
// member is copied; usage and a choice's finish_reason stay on the last
// part only so downstream accounting sees them once.
func (c *chatCodec) Split(raw RawEvent) []RawEvent {
	if raw.Comment || raw.Oversized || !jsonObject(raw.Data) {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw.Data, &top); err != nil {
		return nil
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil || len(choices) == 0 {
		return nil
	}
	var parts []json.RawMessage
	for _, choice := range choices {
		split, ok := splitToolCalls(choice)
		if !ok {
			return nil
		}
		parts = append(parts, split...)
	}
	if len(parts) <= 1 {
		return nil
	}
	out := make([]RawEvent, 0, len(parts))
	for i, choice := range parts {
		part := make(map[string]json.RawMessage, len(top))
		maps.Copy(part, top)
		single, err := json.Marshal([]json.RawMessage{choice})
		if err != nil {
			return nil
		}
		part["choices"] = single
		if i < len(parts)-1 {
			delete(part, "usage")
		}
		data, err := json.Marshal(part)
		if err != nil {
			return nil
		}
		ev := Event{Name: raw.Name, Data: data}
		out = append(out, RawEvent{Name: raw.Name, Data: data, Raw: ev.Encode()})
	}
	return out
}

// textMembers are the delta members Decode classifies on before tool
// calls; a delta carrying one of them alongside tool calls is split so the
// text and every call are transformed on their own.
var textMembers = []string{"content", "reasoning_content", "reasoning"}

// splitToolCalls divides a choice whose delta carries several tool calls, or
// text alongside tool calls, into copies that each carry one thing: the
// text first, then one tool call each. The choice's finish_reason stays on
// the last copy. A choice that needs no splitting is returned as is. ok
// is false when the choice cannot be decoded.
func splitToolCalls(choice json.RawMessage) ([]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(choice, &obj); err != nil {
		return nil, false
	}
	var delta map[string]json.RawMessage
	if raw, ok := obj["delta"]; !ok || !jsonObject(raw) {
		return []json.RawMessage{choice}, true
	} else if err := json.Unmarshal(raw, &delta); err != nil {
		return nil, false
	}
	var calls []json.RawMessage
	if raw, ok := delta["tool_calls"]; !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return []json.RawMessage{choice}, true
	} else if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, false
	}
	hasText := false
	for _, member := range textMembers {
		if text, ok := jsonStringOf(delta[member]); ok && text != "" {
			hasText = true
		}
	}
	if len(calls) == 0 || (len(calls) == 1 && !hasText) {
		return []json.RawMessage{choice}, true
	}
	var deltas []map[string]json.RawMessage
	if hasText {
		textDelta := make(map[string]json.RawMessage, len(delta))
		maps.Copy(textDelta, delta)
		delete(textDelta, "tool_calls")
		deltas = append(deltas, textDelta)
	}
	for _, call := range calls {
		callDelta := make(map[string]json.RawMessage, len(delta))
		maps.Copy(callDelta, delta)
		for _, member := range textMembers {
			delete(callDelta, member)
		}
		single, err := json.Marshal([]json.RawMessage{call})
		if err != nil {
			return nil, false
		}
		callDelta["tool_calls"] = single
		deltas = append(deltas, callDelta)
	}
	// The role announces the message once; it stays on the first part only.
	for _, partDelta := range deltas[1:] {
		delete(partDelta, "role")
	}
	out := make([]json.RawMessage, 0, len(deltas))
	for i, partDelta := range deltas {
		partChoice := make(map[string]json.RawMessage, len(obj))
		maps.Copy(partChoice, obj)
		var err error
		if partChoice["delta"], err = json.Marshal(partDelta); err != nil {
			return nil, false
		}
		if i < len(deltas)-1 && jsonNonNull(partChoice["finish_reason"]) {
			partChoice["finish_reason"] = json.RawMessage("null")
		}
		encoded, err := json.Marshal(partChoice)
		if err != nil {
			return nil, false
		}
		out = append(out, encoded)
	}
	return out, true
}

// Restate is a no-op: chat chunks never repeat streamed text.
func (c *chatCodec) Restate(ev Event) (Event, bool) { return ev, false }

// chatChoicePosition finds the choice whose index member equals want, falling
// back to the positional entry.
func chatChoicePosition(choices []map[string]json.RawMessage, want int) int {
	for pos, choice := range choices {
		if idx, err := strconv.Atoi(string(choice["index"])); err == nil && idx == want {
			return pos
		}
	}
	if want >= 0 && want < len(choices) {
		return want
	}
	if len(choices) > 0 {
		return 0
	}
	return -1
}

type chatFinishChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chatTerminalChunk struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	Provider          string             `json:"provider,omitempty"`
	SystemFingerprint string             `json:"system_fingerprint,omitempty"`
	Choices           []chatFinishChoice `json:"choices"`
	Usage             any                `json:"usage,omitempty"`
}

func (c *chatCodec) chunk(choices []chatFinishChoice) chatTerminalChunk {
	created := c.created
	if created == 0 {
		created = time.Now().Unix()
	}
	return chatTerminalChunk{
		ID:                c.id,
		Object:            "chat.completion.chunk",
		Created:           created,
		Model:             c.model,
		Provider:          c.provider,
		SystemFingerprint: c.fingerprint,
		Choices:           choices,
	}
}

// Terminate ends a chat stream: an optional error payload, an optional final
// text delta, one chunk carrying finish_reason for every choice still open,
// and [DONE].
func (c *chatCodec) Terminate(t Termination) [][]byte {
	var out [][]byte
	if t.ErrorCode != "" {
		payload := map[string]any{"error": map[string]any{
			"message": t.ErrorMessage,
			"type":    "server_error",
			"code":    t.ErrorCode,
		}}
		if encoded, err := encodeJSONEvent("", payload); err == nil {
			out = append(out, encoded)
		}
	}
	open := make([]int, 0, len(c.choices))
	for _, idx := range c.choices {
		if !c.finished[idx] {
			open = append(open, idx)
		}
	}
	// A stream that showed no choice at all, or a final text that needs a
	// carrier, is closed on choice 0. Choices the provider already finished
	// get no second finish_reason.
	if len(open) == 0 && (len(c.choices) == 0 || t.Text != "") {
		open = []int{0}
	}
	if t.Text != "" {
		chunk := c.chunk([]chatFinishChoice{{Index: open[0], Delta: map[string]any{"content": t.Text}}})
		if encoded, err := encodeJSONEvent("", chunk); err == nil {
			out = append(out, encoded)
		}
	}
	reason := t.finishReason()
	choices := make([]chatFinishChoice, 0, len(open))
	for _, idx := range open {
		choices = append(choices, chatFinishChoice{Index: idx, Delta: map[string]any{}, FinishReason: &reason})
		c.finished[idx] = true
	}
	if len(choices) > 0 || t.Usage != nil {
		chunk := c.chunk(choices)
		chunk.Usage = t.Usage
		if encoded, err := encodeJSONEvent("", chunk); err == nil {
			out = append(out, encoded)
		}
	}
	return append(out, doneEventBytes)
}
