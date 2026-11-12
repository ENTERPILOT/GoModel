package server

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
)

// responseCostMember is the member a streamed usage object carries the cost
// in. It is namespaced so it never collides with a cost the provider reports
// itself, such as OpenRouter's usage.cost.
const responseCostMember = "gomodel_cost"

// appendEvent appends ev to out, with the cost known so far added to the
// usage object it carries. Every event without one passes byte for byte.
func (w *responseCostWriter) appendEvent(out []byte, ev streaming.RawEvent) []byte {
	if ev.Comment || ev.Oversized || !mayHoldUsage(ev.Data) {
		return append(out, ev.Raw...)
	}
	amount, known := w.report.Cost()
	data, ok := withUsageCost(ev.Data, amount, known)
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

// withUsageCost returns the payload with this gateway's cost on its usage
// objects; ok is false when the payload is left as it is. The members are
// spliced into the payload's bytes rather than decoding and re-encoding it, so
// the rest stays as the provider formatted it.
//
// A cost member an upstream set never survives, whichever member a parser
// keeps: every member named gomodel_cost in any case or spelling is removed
// from every usage object of the payload, duplicates included. With the cost
// known, each usage object at the top level (a chat chunk, an Anthropic
// message_delta) or under "response" (a Responses terminal event) then gets
// exactly one member with it. A usage object elsewhere, such as the one in an
// Anthropic message_start, only loses an upstream's member.
func withUsageCost(data []byte, amount float64, known bool) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return nil, false
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return nil, false
	}
	// Parse drops leading whitespace from Raw without counting it in Index.
	root.Index = len(data) - len(bytes.TrimLeft(data, " \t\r\n"))
	member := ""
	if known {
		member = `"` + responseCostMember + `":{"amount":` + formatResponseCost(amount) + `,"currency":"` + usage.ResponseCostCurrency + `"}`
	}
	var edits []splice
	root.ForEach(func(key, value gjson.Result) bool {
		switch {
		case strings.EqualFold(key.Str, "usage"):
			edits = appendUsageCostEdit(edits, value, memberFor(key.Str == "usage", member))
		case strings.EqualFold(key.Str, "response"), strings.EqualFold(key.Str, "message"):
			carries := key.Str == "response"
			value.ForEach(func(nested, object gjson.Result) bool {
				if strings.EqualFold(nested.Str, "usage") {
					edits = appendUsageCostEdit(edits, object, memberFor(carries && nested.Str == "usage", member))
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

// memberFor returns member for a usage object that carries the cost, and
// nothing for one that only loses an upstream's.
func memberFor(carries bool, member string) string {
	if carries {
		return member
	}
	return ""
}

// splice replaces data[from:to] with text.
type splice struct {
	from, to int
	text     string
}

// appendUsageCostEdit appends the edit that leaves the usage object with
// member as its only cost member, or none when member is empty.
func appendUsageCostEdit(edits []splice, object gjson.Result, member string) []splice {
	if !object.IsObject() {
		return edits
	}
	var kept []string
	removed := false
	object.ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.Str, responseCostMember) {
			removed = true
			return true
		}
		kept = append(kept, object.Raw[key.Index-object.Index:value.Index-object.Index+len(value.Raw)])
		return true
	})
	end := object.Index + len(object.Raw)
	if !removed {
		if member == "" {
			return edits
		}
		if strings.TrimSpace(object.Raw[1:len(object.Raw)-1]) != "" {
			member = "," + member
		}
		return append(edits, splice{from: end - 1, to: end - 1, text: member})
	}
	if member != "" {
		kept = append(kept, member)
	}
	return append(edits, splice{from: object.Index, to: end, text: "{" + strings.Join(kept, ",") + "}"})
}

// applySplices returns data with the edits, in ascending order and not
// overlapping, applied.
func applySplices(data []byte, edits []splice) []byte {
	out := make([]byte, 0, len(data)+64*len(edits))
	at := 0
	for _, edit := range edits {
		out = append(out, data[at:edit.from]...)
		out = append(out, edit.text...)
		at = edit.to
	}
	return append(out, data[at:]...)
}
