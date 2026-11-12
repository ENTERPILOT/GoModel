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
	if ev.Comment || ev.Oversized || !mayHoldUsage(ev.Data) {
		return append(out, ev.Raw...)
	}
	amount, known := w.report.Cost()
	data, ok := withUsageCost(ev.Data, newUsageCostEdit(amount, known, w.billed))
	if !ok {
		return append(out, ev.Raw...)
	}
	return appendWithData(out, ev.Raw, data)
}

// mayHoldUsage reports whether a payload can hold a member some JSON decoder
// reads as "usage": the name in any case (Go matches struct fields
// case-insensitively, and folds ſ to s), or spelled with escapes. Text deltas
// mostly fail this check, so they are never parsed.
func mayHoldUsage(data []byte) bool {
	return containsFoldASCII(data, "usage") ||
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

// withUsageCost returns the event payload with edit applied to its usage
// objects; ok is false when the payload is left as it is. Only the usage
// objects are rewritten, in place, so the rest of the payload stays as the
// provider formatted it.
//
// Every usage object of the payload loses the cost members an upstream set
// (see usageCostEdit), duplicates and case variants included. Each one named
// exactly "usage" at the top level (a chat chunk, an Anthropic message_delta)
// or under "response" (a Responses terminal event) then carries this
// gateway's cost. A usage object elsewhere, such as the one in an Anthropic
// message_start, only loses members.
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
	root.ForEach(func(key, value gjson.Result) bool {
		switch {
		case strings.EqualFold(key.Str, "usage"):
			edits = appendUsageSplice(edits, value, edit.carrying(key.Str == "usage"))
		case strings.EqualFold(key.Str, "response"), strings.EqualFold(key.Str, "message"):
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
