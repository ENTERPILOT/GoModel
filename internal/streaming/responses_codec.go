package streaming

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// responsesEventView decodes the members of a Responses API stream event the
// codec classifies on.
type responsesEventView struct {
	Type           string          `json:"type"`
	Delta          string          `json:"delta"`
	OutputIndex    int             `json:"output_index"`
	ContentIndex   int             `json:"content_index"`
	SummaryIndex   int             `json:"summary_index"`
	SequenceNumber *int            `json:"sequence_number"`
	Item           json.RawMessage `json:"item"`
	Response       *struct {
		ID        string `json:"id"`
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		CreatedAt int64  `json:"created_at"`
	} `json:"response"`
}

// responsesItem tracks an output item announced by response.output_item.added
// so a cut stream can close it.
type responsesItem struct {
	index        int
	id           string
	itemType     string
	raw          json.RawMessage
	text         strings.Builder
	arguments    strings.Builder
	contentIndex int
	partOpen     bool
	done         bool
	// parts and summaries hold the text emitted into each content part
	// (output_text, reasoning_text) and reasoning summary part, so the
	// events that restate a part's full text can be rewritten to match.
	parts     map[int]*strings.Builder
	summaries map[int]*strings.Builder
}

func (item *responsesItem) part(index int) *strings.Builder {
	if item.parts == nil {
		item.parts = map[int]*strings.Builder{}
	}
	if b := item.parts[index]; b != nil {
		return b
	}
	b := &strings.Builder{}
	item.parts[index] = b
	return b
}

func (item *responsesItem) summary(index int) *strings.Builder {
	if item.summaries == nil {
		item.summaries = map[int]*strings.Builder{}
	}
	if b := item.summaries[index]; b != nil {
		return b
	}
	b := &strings.Builder{}
	item.summaries[index] = b
	return b
}

type responsesCodec struct {
	id, model, provider string
	createdAt           int64
	seq                 int
	items               map[int]*responsesItem
	order               []int
	// textIndex and argsIndex are the output_index of the most recently
	// decoded text and function-call delta; emitted deltas (which may be
	// re-segmented copies) are attributed to them by Track. textPart is
	// the content or summary index of that text delta and textSummary says
	// which of the two it is.
	textIndex, argsIndex int
	textPart             int
	textSummary          bool
}

// ResponsesCodec returns a codec for Responses API event streams.
func ResponsesCodec() Codec {
	return &responsesCodec{items: make(map[int]*responsesItem)}
}

func (c *responsesCodec) Decode(raw RawEvent, seq int) Event {
	if raw.Comment || raw.Oversized || len(raw.Data) == 0 || raw.Data[0] != '{' {
		return decodeOther(raw, seq)
	}
	var view responsesEventView
	if err := json.Unmarshal(raw.Data, &view); err != nil {
		return decodeOther(raw, seq)
	}
	c.remember(&view)

	ev := Event{Seq: seq, Kind: KindOther, Name: raw.Name, Data: raw.Data}
	switch view.Type {
	case "response.output_text.delta":
		ev.Kind, ev.Text = KindTextDelta, view.Delta
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		ev.Kind, ev.Text = KindReasoningDelta, view.Delta
	case "response.function_call_arguments.delta":
		ev.Kind, ev.Text = KindToolCallDelta, view.Delta
	case "response.completed", "response.incomplete", "response.failed":
		ev.Kind = KindFinish
	}
	return ev
}

func (c *responsesCodec) remember(view *responsesEventView) {
	if view.SequenceNumber != nil && *view.SequenceNumber >= c.seq {
		c.seq = *view.SequenceNumber + 1
	}
	if resp := view.Response; resp != nil {
		if resp.ID != "" {
			c.id = resp.ID
		}
		if resp.Model != "" {
			c.model = resp.Model
		}
		if resp.Provider != "" {
			c.provider = resp.Provider
		}
		if resp.CreatedAt != 0 {
			c.createdAt = resp.CreatedAt
		}
	}
	switch view.Type {
	case "response.output_text.delta", "response.reasoning_text.delta":
		c.textIndex, c.textPart, c.textSummary = view.OutputIndex, view.ContentIndex, false
	case "response.reasoning_summary_text.delta":
		c.textIndex, c.textPart, c.textSummary = view.OutputIndex, view.SummaryIndex, true
	case "response.function_call_arguments.delta":
		c.argsIndex = view.OutputIndex
	}
}

// Track follows the output items the client has seen and the text emitted
// into them.
func (c *responsesCodec) Track(ev Event) {
	switch ev.Kind {
	case KindTextDelta:
		if item := c.items[c.textIndex]; item != nil {
			item.text.WriteString(ev.Text)
			item.part(c.textPart).WriteString(ev.Text)
		}
		return
	case KindReasoningDelta:
		if item := c.items[c.textIndex]; item != nil {
			if c.textSummary {
				item.summary(c.textPart).WriteString(ev.Text)
			} else {
				item.part(c.textPart).WriteString(ev.Text)
			}
		}
		return
	case KindToolCallDelta:
		if item := c.items[c.argsIndex]; item != nil {
			item.arguments.WriteString(ev.Text)
		}
		return
	case KindOther:
	default:
		return
	}
	if !isResponsesStructuralEvent(ev) {
		return
	}
	var view responsesEventView
	if err := json.Unmarshal(ev.Data, &view); err != nil {
		return
	}
	switch view.Type {
	case "response.output_item.added":
		item := &responsesItem{index: view.OutputIndex, raw: append(json.RawMessage(nil), view.Item...)}
		var head struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(view.Item, &head)
		item.id, item.itemType = head.ID, head.Type
		if _, seen := c.items[view.OutputIndex]; !seen {
			c.order = append(c.order, view.OutputIndex)
		}
		c.items[view.OutputIndex] = item
	case "response.output_item.done":
		if item := c.items[view.OutputIndex]; item != nil {
			item.done = true
		}
	case "response.content_part.added":
		if item := c.items[view.OutputIndex]; item != nil {
			item.partOpen, item.contentIndex = true, view.ContentIndex
		}
	case "response.content_part.done":
		if item := c.items[view.OutputIndex]; item != nil {
			item.partOpen = false
		}
	}
}

func (c *responsesCodec) RewriteText(ev Event, text string) (Event, error) {
	if ev.Kind != KindTextDelta && ev.Kind != KindReasoningDelta {
		return ev, ErrNotTextEvent
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &top); err != nil {
		return ev, fmt.Errorf("streaming: decode responses event: %w", err)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return ev, err
	}
	top["delta"] = encoded
	data, err := json.Marshal(top)
	if err != nil {
		return ev, err
	}
	ev.Text = text
	ev.Data = data
	return ev, nil
}

// Split is a no-op: a Responses event carries one delta.
func (c *responsesCodec) Split(RawEvent) []RawEvent { return nil }

// Restate rewrites the events that repeat streamed text (the *.done events
// of a part or item and the terminal response.* event) so their text is
// what was emitted after transformation. An event whose text already
// matches is returned unchanged.
func (c *responsesCodec) Restate(ev Event) (Event, bool) {
	if (ev.Kind != KindOther && ev.Kind != KindFinish) || len(ev.Data) == 0 || ev.Data[0] != '{' || !isResponsesRestatingEvent(ev) {
		return ev, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &top); err != nil {
		return ev, false
	}
	var view responsesEventView
	if err := json.Unmarshal(ev.Data, &view); err != nil {
		return ev, false
	}
	changed := false
	switch view.Type {
	case "response.output_text.done", "response.reasoning_text.done":
		changed = c.restateText(top, "text", view.OutputIndex, view.ContentIndex, false)
	case "response.reasoning_summary_text.done":
		changed = c.restateText(top, "text", view.OutputIndex, view.SummaryIndex, true)
	case "response.content_part.done":
		changed = c.restateNested(top, "part", func(part map[string]json.RawMessage) bool {
			return c.restateText(part, "text", view.OutputIndex, view.ContentIndex, false)
		})
	case "response.reasoning_summary_part.done":
		changed = c.restateNested(top, "part", func(part map[string]json.RawMessage) bool {
			return c.restateText(part, "text", view.OutputIndex, view.SummaryIndex, true)
		})
	case "response.output_item.done":
		changed = c.restateNested(top, "item", func(item map[string]json.RawMessage) bool {
			return c.restateItem(item, c.items[view.OutputIndex])
		})
	case "response.completed", "response.incomplete", "response.failed":
		changed = c.restateNested(top, "response", c.restateOutput)
	}
	if !changed {
		return ev, false
	}
	data, err := json.Marshal(top)
	if err != nil {
		return ev, false
	}
	ev.Data = data
	return ev, true
}

// restateText sets obj[key] to the emitted text of the part when one was
// tracked and differs.
func (c *responsesCodec) restateText(obj map[string]json.RawMessage, key string, output, part int, summary bool) bool {
	item := c.items[output]
	if item == nil {
		return false
	}
	var b *strings.Builder
	if summary {
		b = item.summaries[part]
	} else {
		b = item.parts[part]
	}
	if b == nil {
		return false
	}
	if current, ok := jsonStringOf(obj[key]); ok && current == b.String() {
		return false
	}
	encoded, err := json.Marshal(b.String())
	if err != nil {
		return false
	}
	obj[key] = encoded
	return true
}

// restateNested decodes obj[key] as an object, lets fn edit it, and writes
// it back when fn reports a change.
func (c *responsesCodec) restateNested(obj map[string]json.RawMessage, key string, fn func(map[string]json.RawMessage) bool) bool {
	raw, ok := obj[key]
	if !ok || len(raw) == 0 || raw[0] != '{' {
		return false
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil || !fn(nested) {
		return false
	}
	encoded, err := json.Marshal(nested)
	if err != nil {
		return false
	}
	obj[key] = encoded
	return true
}

// restateItem rewrites the content parts and reasoning summaries of an
// output item object to the text emitted into them.
func (c *responsesCodec) restateItem(obj map[string]json.RawMessage, item *responsesItem) bool {
	if item == nil {
		return false
	}
	changed := false
	for _, member := range []struct {
		key     string
		emitted map[int]*strings.Builder
	}{{"content", item.parts}, {"summary", item.summaries}} {
		raw, ok := obj[member.key]
		if !ok || len(member.emitted) == 0 || len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		entryChanged := false
		for i, entry := range entries {
			b := member.emitted[i]
			if b == nil {
				continue
			}
			if current, ok := jsonStringOf(entry["text"]); !ok || current == b.String() {
				continue
			}
			encoded, err := json.Marshal(b.String())
			if err != nil {
				continue
			}
			entry["text"] = encoded
			entryChanged = true
		}
		if !entryChanged {
			continue
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			continue
		}
		obj[member.key] = encoded
		changed = true
	}
	return changed
}

// restateOutput rewrites the items of a terminal response object, matched
// to the tracked items by id and otherwise by position.
func (c *responsesCodec) restateOutput(resp map[string]json.RawMessage) bool {
	raw, ok := resp["output"]
	if !ok || len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return false
	}
	changed := false
	for pos, entry := range entries {
		item := c.items[pos]
		if id, ok := jsonStringOf(entry["id"]); ok && id != "" {
			for _, candidate := range c.items {
				if candidate.id == id {
					item = candidate
					break
				}
			}
		}
		if c.restateItem(entry, item) {
			changed = true
		}
	}
	if !changed {
		return false
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return false
	}
	resp["output"] = encoded
	return true
}

// isResponsesRestatingEvent reports whether ev may restate streamed text,
// from its event name when present and otherwise from a byte scan.
func isResponsesRestatingEvent(ev Event) bool {
	if ev.Name != "" {
		return strings.HasSuffix(ev.Name, ".done") || ev.Name == "response.completed" || ev.Name == "response.incomplete" || ev.Name == "response.failed"
	}
	return bytes.Contains(ev.Data, []byte(`.done"`)) || bytes.Contains(ev.Data, []byte(`"response.completed"`)) ||
		bytes.Contains(ev.Data, []byte(`"response.incomplete"`)) || bytes.Contains(ev.Data, []byte(`"response.failed"`))
}

// emit appends one event with the next sequence_number.
func (c *responsesCodec) emit(out [][]byte, name string, payload map[string]any) [][]byte {
	payload["type"] = name
	payload["sequence_number"] = c.seq
	c.seq++
	encoded, err := encodeJSONEvent(name, payload)
	if err != nil {
		return out
	}
	return append(out, encoded)
}

// Terminate ends a Responses stream: an optional final text delta, the
// closing events of every open output item, response.incomplete (or
// response.failed when ErrorCode is set) with the output seen so far, and
// [DONE].
func (c *responsesCodec) Terminate(t Termination) [][]byte {
	var out [][]byte
	status := "incomplete"
	if t.ErrorCode != "" {
		status = "failed"
	}
	if t.Text != "" {
		out = c.emitFinalText(out, t.Text)
	}
	output := make([]any, 0, len(c.order))
	for _, index := range c.order {
		item := c.items[index]
		out = c.closeItem(out, item, status)
		output = append(output, c.itemObject(item, status))
	}
	createdAt := c.createdAt
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	response := map[string]any{
		"id":         c.id,
		"object":     "response",
		"created_at": createdAt,
		"model":      c.model,
		"provider":   c.provider,
		"status":     status,
		"output":     output,
	}
	if t.Usage != nil {
		response["usage"] = t.Usage
	}
	name := "response.incomplete"
	if t.ErrorCode != "" {
		name = "response.failed"
		response["error"] = map[string]any{"code": t.ErrorCode, "message": t.ErrorMessage}
	} else {
		response["incomplete_details"] = map[string]any{"reason": t.finishReason()}
	}
	out = c.emit(out, name, map[string]any{"response": response})
	return append(out, doneEventBytes)
}

// emitFinalText writes text into the open message item, opening a new one
// when none is open.
func (c *responsesCodec) emitFinalText(out [][]byte, text string) [][]byte {
	var item *responsesItem
	for _, v := range slices.Backward(c.order) {
		if candidate := c.items[v]; candidate.itemType == "message" && !candidate.done {
			item = candidate
			break
		}
	}
	if item == nil {
		index := 0
		if n := len(c.order); n > 0 {
			index = c.order[n-1] + 1
		}
		item = &responsesItem{index: index, id: fmt.Sprintf("msg_%s_%d", c.id, index), itemType: "message"}
		c.items[index] = item
		c.order = append(c.order, index)
		out = c.emit(out, "response.output_item.added", map[string]any{
			"output_index": index,
			"item":         c.itemObject(item, "in_progress"),
		})
	}
	if !item.partOpen {
		item.partOpen = true
		out = c.emit(out, "response.content_part.added", map[string]any{
			"item_id":       item.id,
			"output_index":  item.index,
			"content_index": item.contentIndex,
			"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
	}
	item.text.WriteString(text)
	return c.emit(out, "response.output_text.delta", map[string]any{
		"item_id":       item.id,
		"output_index":  item.index,
		"content_index": item.contentIndex,
		"delta":         text,
	})
}

func (c *responsesCodec) closeItem(out [][]byte, item *responsesItem, status string) [][]byte {
	if item.done {
		return out
	}
	item.done = true
	switch item.itemType {
	case "message":
		if item.partOpen {
			out = c.emit(out, "response.output_text.done", map[string]any{
				"item_id":       item.id,
				"output_index":  item.index,
				"content_index": item.contentIndex,
				"text":          item.text.String(),
			})
			out = c.emit(out, "response.content_part.done", map[string]any{
				"item_id":       item.id,
				"output_index":  item.index,
				"content_index": item.contentIndex,
				"part":          map[string]any{"type": "output_text", "text": item.text.String(), "annotations": []any{}},
			})
		}
	case "function_call":
		out = c.emit(out, "response.function_call_arguments.done", map[string]any{
			"item_id":      item.id,
			"output_index": item.index,
			"arguments":    item.arguments.String(),
		})
	}
	return c.emit(out, "response.output_item.done", map[string]any{
		"output_index": item.index,
		"item":         c.itemObject(item, status),
	})
}

// itemObject rebuilds the output item from the added event plus the text or
// arguments accumulated since.
func (c *responsesCodec) itemObject(item *responsesItem, status string) map[string]any {
	obj := map[string]any{}
	if len(item.raw) > 0 {
		_ = json.Unmarshal(item.raw, &obj)
	}
	obj["id"] = item.id
	obj["type"] = item.itemType
	obj["status"] = status
	switch item.itemType {
	case "message":
		obj["role"] = "assistant"
		if _, ok := obj["content"]; !ok || item.text.Len() > 0 {
			obj["content"] = []any{map[string]any{"type": "output_text", "text": item.text.String(), "annotations": []any{}}}
		}
	case "function_call":
		if item.arguments.Len() > 0 {
			obj["arguments"] = item.arguments.String()
		}
	}
	return obj
}

// isResponsesStructuralEvent reports whether ev may open or close an output
// item or content part, from its event name when present and otherwise
// from a byte scan, so Track decodes only those.
func isResponsesStructuralEvent(ev Event) bool {
	if ev.Name != "" {
		return strings.HasPrefix(ev.Name, "response.output_item.") || strings.HasPrefix(ev.Name, "response.content_part.")
	}
	return bytes.Contains(ev.Data, []byte(`"response.output_item.`)) || bytes.Contains(ev.Data, []byte(`"response.content_part.`))
}
