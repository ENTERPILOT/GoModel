package streaming

import (
	"fmt"
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
	if raw.Comment || raw.Oversized || len(raw.Data) == 0 || raw.Data[0] != '{' {
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
				if fn := delta.ToolCalls[0].Function; fn != nil {
					ev.Text = fn.Arguments
				}
				return ev
			}
		}
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

// Track marks the choice of an emitted finish chunk as closed.
func (c *chatCodec) Track(ev Event) {
	if ev.Kind == KindFinish {
		c.finished[ev.Choice] = true
	}
}

func (c *chatCodec) RewriteText(ev Event, text string) (Event, error) {
	if ev.Kind != KindTextDelta && ev.Kind != KindReasoningDelta {
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
	key := "content"
	if ev.Kind == KindReasoningDelta {
		key = "reasoning_content"
		if _, ok := delta["reasoning_content"]; !ok {
			if _, ok := delta["reasoning"]; ok {
				key = "reasoning"
			}
		}
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return ev, err
	}
	delta[key] = encoded
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
	if len(open) == 0 {
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
	chunk := c.chunk(choices)
	chunk.Usage = t.Usage
	if encoded, err := encodeJSONEvent("", chunk); err == nil {
		out = append(out, encoded)
	}
	return append(out, doneEventBytes)
}
