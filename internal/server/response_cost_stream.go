package server

import (
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
	if ev.Comment || ev.Oversized || !usage.HasUsageObject(ev.Data) {
		return append(out, ev.Raw...)
	}
	amount, known := w.report.Cost()
	data, ok := withUsageCost(ev.Data, amount, known)
	if !ok {
		return append(out, ev.Raw...)
	}
	return appendWithData(out, ev.Raw, data)
}

// withUsageCost returns the event payload with the cost set on its usage
// object; ok is false when the payload is left as it is. The member is
// spliced into the payload's bytes rather than decoding and re-encoding it,
// so the rest stays as the provider formatted it. A member an upstream
// GoModel already set is replaced: by this gateway's cost, or by null when
// that is unknown, so a caller never reads the upstream's cost as its own.
func withUsageCost(data []byte, amount float64, known bool) ([]byte, bool) {
	start, usageObject, ok := streamedUsageObject(data)
	if !ok {
		return nil, false
	}
	value := "null"
	if known {
		value = `{"amount":` + formatResponseCost(amount) + `,"currency":"` + usage.ResponseCostCurrency + `"}`
	}
	if existing := gjson.Get(usageObject, responseCostMember); existing.Exists() && existing.Index > 0 {
		at := start + existing.Index
		return spliceBytes(data, at, at+len(existing.Raw), value), true
	}
	if !known {
		return nil, false
	}
	member := `"` + responseCostMember + `":` + value
	if strings.TrimSpace(usageObject[1:len(usageObject)-1]) != "" {
		member = "," + member
	}
	end := start + len(usageObject) - 1
	return spliceBytes(data, end, end, member), true
}

// streamedUsageObject locates the usage object of a streamed event: at the
// top level in a chat completion chunk and an Anthropic message_delta, under
// "response" in the terminal events of a Responses stream. It returns the
// object's offset in data and its text.
func streamedUsageObject(data []byte) (int, string, bool) {
	if !gjson.ValidBytes(data) {
		return 0, "", false
	}
	if found := gjson.GetBytes(data, "usage"); found.IsObject() && found.Index > 0 {
		return found.Index, found.Raw, true
	}
	response := gjson.GetBytes(data, "response")
	if !response.IsObject() || response.Index == 0 {
		return 0, "", false
	}
	found := gjson.Get(response.Raw, "usage")
	if !found.IsObject() || found.Index == 0 {
		return 0, "", false
	}
	return response.Index + found.Index, found.Raw, true
}

// spliceBytes returns data with data[from:to] replaced by value.
func spliceBytes(data []byte, from, to int, value string) []byte {
	out := make([]byte, 0, len(data)-(to-from)+len(value))
	out = append(out, data[:from]...)
	out = append(out, value...)
	return append(out, data[to:]...)
}
