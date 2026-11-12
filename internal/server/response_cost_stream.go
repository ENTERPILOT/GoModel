package server

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// appendEvent appends ev to out, with the cost known so far on the usage
// object it carries. Every event without one passes byte for byte.
func (w *responseCostWriter) appendEvent(out []byte, ev streaming.RawEvent) []byte {
	if ev.Comment || ev.Oversized || !mayHoldCost(ev.Data, w.billed) {
		return append(out, ev.Raw...)
	}
	amount, known := w.report.Cost()
	data, ok := withUsageCost(ev.Data, newUsageCostEdit(amount, known, w.billed))
	if !ok {
		return append(out, ev.Raw...)
	}
	return appendEventWithData(out, ev.Raw, data)
}

// appendEventWithData appends the SSE block raw with its data lines replaced
// by data, one data line per line of it. Every other byte stays in place: the
// other fields (event, id, retry, comments), so reconnection metadata survives
// the rewrite, and each line's own ending, CRLF, LF or a bare CR, so a client
// finds the end of the block where the upstream put it.
func appendEventWithData(out, raw, data []byte) []byte {
	written := false
	for len(raw) > 0 {
		var line, ending []byte
		line, ending, raw = streaming.CutLine(raw)
		if !bytes.HasPrefix(line, []byte("data:")) {
			out = append(append(out, line...), ending...)
			continue
		}
		if written {
			continue
		}
		written = true
		// An unterminated last line has no ending to repeat between lines.
		between := ending
		if len(between) == 0 {
			between = []byte("\n")
		}
		for rest := data; ; {
			part, tail, more := bytes.Cut(rest, []byte("\n"))
			out = append(append(out, "data: "...), part...)
			if !more {
				break
			}
			out, rest = append(out, between...), tail
		}
		out = append(out, ending...)
	}
	return out
}

// mayHoldCost reports whether a payload can hold a member the edit rewrites:
// one some JSON decoder reads as "usage" and, with billed, as a provider's
// "cost" beside it. A name is matched in any case (Go matches struct fields
// case-insensitively, and folds ſ to s), or spelled with escapes. Text deltas
// mostly fail this check, so they are never parsed.
func mayHoldCost(data []byte, billed bool) bool {
	return containsFoldASCII(data, "usage") ||
		(billed && containsFoldASCII(data, usageCostMember)) ||
		bytes.Contains(data, []byte(`\u`)) ||
		bytes.Contains(data, []byte("ſ"))
}

// containsFoldASCII reports whether data contains word, a lower-case ASCII
// word, in any ASCII case.
func containsFoldASCII(data []byte, word string) bool {
	for i := 0; i+len(word) <= len(data); i++ {
		j := 0
		for j < len(word) && data[i+j]|0x20 == word[j] {
			j++
		}
		if j == len(word) {
			return true
		}
	}
	return false
}

// withUsageCost returns a JSON object, an event payload or a whole body, with
// edit applied; ok is false when it is left as it is. Only the usage objects
// and the removed members change, in place, so the rest stays as the provider
// formatted it. jsonUsageEditor does the same to a body as it passes through.
//
// Three usage objects are rewritten: the one at the top level (a chat chunk,
// an Anthropic message_delta), the one under "response" (a Responses terminal
// event) and the one under "message" (an Anthropic message_start). Each loses
// the cost members an upstream set (see usageCostEdit), duplicates and case
// variants included. Each one named exactly "usage", at the top level or
// under exactly "response", then carries this gateway's cost; the others only
// lose members. With billed, a provider's cost at the top level is removed
// too. A usage object anywhere else is not read.
func withUsageCost(data []byte, edit usageCostEdit) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return nil, false
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return nil, false
	}
	// Parse drops leading whitespace from Raw without counting it in Index.
	root.Index = len(data) - len(bytes.TrimLeft(data, " \t\r\n"))
	var edits []splice
	// The end of the member before, and whether one before it is kept: a
	// removed member takes the separator in front of it along, and the first
	// member kept has none (as in usageCostEdit.apply).
	before, kept := -1, false
	root.ForEach(func(key, value gjson.Result) bool {
		drop := edit.dropsRootMember(key.Str)
		end := value.Index + len(value.Raw)
		if before >= 0 && (drop || !kept) {
			edits = append(edits, splice{from: before, to: key.Index})
		}
		before, kept = end, kept || !drop
		switch {
		case drop:
			edits = append(edits, splice{from: key.Index, to: end})
		case strings.EqualFold(key.Str, "usage"):
			edits = appendUsageSplice(edits, value, edit.carrying(key.Str == "usage"))
		case isUsageEnvelope(key.Str) && value.IsObject():
			carries := key.Str == "response"
			value.ForEach(func(nested, object gjson.Result) bool {
				if strings.EqualFold(nested.Str, "usage") {
					edits = appendUsageSplice(edits, object, edit.carrying(carries && nested.Str == "usage"))
				}
				return true
			})
		}
		return true
	})
	if len(edits) == 0 {
		return nil, false
	}
	return applySplices(data, edits), true
}

// isUsageEnvelope reports whether a top-level member named name may hold a
// usage object of its own: a Responses event's response, an Anthropic
// message_start's message.
func isUsageEnvelope(name string) bool {
	return strings.EqualFold(name, "response") || strings.EqualFold(name, "message")
}

// splice replaces data[from:to] with text.
type splice struct {
	from, to int
	text     string
}

// appendUsageSplice appends the splice that applies edit to the usage object.
func appendUsageSplice(edits []splice, object gjson.Result, edit usageCostEdit) []splice {
	if !object.IsObject() {
		return edits
	}
	text, ok := edit.apply(object.Raw)
	if !ok {
		return edits
	}
	return append(edits, splice{from: object.Index, to: object.Index + len(object.Raw), text: text})
}

// applySplices returns data with the edits, in ascending order and not
// overlapping, applied.
func applySplices(data []byte, edits []splice) []byte {
	out := make([]byte, 0, len(data)+32*len(edits))
	at := 0
	for _, edit := range edits {
		out = append(out, data[at:edit.from]...)
		out = append(out, edit.text...)
		at = edit.to
	}
	return append(out, data[at:]...)
}
