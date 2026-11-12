package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// splitBodies are JSON bodies that exercise every state of the scanner:
// usage objects at each place it reads them, a provider's cost at the top
// level, whitespace around every token, and bodies that are no JSON object.
var splitBodies = []string{
	`{"id":"x","usage":{"prompt_tokens":1,"cost":9,"cost_details":{"a":1},"cost_in_usd_ticks":7}}`,
	`{"cost":0.0009,"id":"x","usage":{"total_tokens":3,"raw_usage":{"cost":9,"n":1}}}`,
	`{"id":"x","cost":0.0009,"usage":{"total_tokens":3},"COST":{"a":[1,{"b":"}"}]},"cost":"9,}"}`,
	`{"cost":1,"Cost":2}`,
	"{ \"cost\" : 1 , \"a\" : \"\\\"}\" ,\n \"usage\" : { \"cost\" : 2 } , \"coſt\" : null }\n",
	`{"type":"response.completed","response":{"id":"r","usage":{"input_tokens":1,"cost":9},"Usage":{"cost":8}},"message":{"usage":{"cost":7,"n":1}}}`,
	`{"response":[{"usage":{"cost":1}}],"message":"usage","usage":null,"Usage":[{"cost":2}]}`,
	`{"response":{"response":{"usage":{"cost":1}},"usage":{}},"usage":{"usage":{"cost":2}}}`,
	`{"a_member_name_that_is_far_too_long_to_be_any_of_the_names_the_scanner_reads_at_all":1,"cost":2,"usage":{"cost":3}}`,
	`{}`,
	` {"usage":{}} `,
	`[{"usage":{"cost":1}},{"cost":2}]`,
	`"usage"`,
	`{"usage":{"cost":1,"total_tokens":`,
	`{"id":"x","cost":0.00`,
	`{"id":"x" , "co`,
	`{"a":1 2,"cost":3}{"cost":4}`,
	`{"usage":{]},"cost":[}`,
}

// editedBody is what the scanner makes of body under edit, written whole.
func editedBody(edit usageCostEdit, body string) string {
	return editBodyPieces(&jsonUsageEditor{edit: edit}, body)
}

var splitEdits = []struct {
	name string
	edit usageCostEdit
}{
	{name: "priced", edit: newUsageCostEdit(0.5, true, false)},
	{name: "billed", edit: newUsageCostEdit(0.75, true, true)},
	{name: "unknown", edit: newUsageCostEdit(0, false, false)},
	{name: "billed unknown", edit: newUsageCostEdit(0, false, true)},
}

// However the writes split a body, the scanner relays the same bytes: every
// way to cut each body in one, two and three pieces is tried.
func TestJSONUsageEditor_EverySplitGivesTheSameBody(t *testing.T) {
	for _, mode := range splitEdits {
		for _, body := range splitBodies {
			want := editedBody(mode.edit, body)
			for i := 0; i <= len(body); i++ {
				for j := i; j <= len(body); j++ {
					got := editBodyPieces(&jsonUsageEditor{edit: mode.edit}, body[:i], body[i:j], body[j:])
					require.Equal(t, want, got, "%s: %q | %q | %q", mode.name, body[:i], body[i:j], body[j:])
				}
			}
		}
	}
}

// A body and an event are rewritten by the same rules: for every JSON object
// the scanner gives what withUsageCost gives for it whole.
func TestJSONUsageEditor_AgreesWithEvents(t *testing.T) {
	for _, mode := range splitEdits {
		for _, body := range splitBodies {
			if !gjson.Valid(body) || !gjson.Parse(body).IsObject() {
				continue
			}
			want := body
			if edited, ok := withUsageCost([]byte(body), mode.edit); ok {
				want = string(edited)
			}
			assert.Equal(t, want, editedBody(mode.edit, body), "%s: %s", mode.name, body)
			assert.True(t, gjson.Valid(want), "%s: %s", mode.name, want)
		}
	}
}

// FuzzJSONUsageEditor_Splits checks both properties for any body: its seeds
// run with every `go test`.
func FuzzJSONUsageEditor_Splits(f *testing.F) {
	for _, body := range splitBodies {
		f.Add(body, uint(len(body)/3), uint(len(body)/2), true)
		f.Add(body, uint(1), uint(2), false)
	}
	f.Fuzz(func(t *testing.T, body string, i, j uint, billed bool) {
		edit := newUsageCostEdit(0.5, true, billed)
		first := min(int(i%uint(len(body)+1)), len(body))
		second := max(first, min(int(j%uint(len(body)+1)), len(body)))
		whole := editedBody(edit, body)

		got := editBodyPieces(&jsonUsageEditor{edit: edit}, body[:first], body[first:second], body[second:])

		require.Equal(t, whole, got)
		if gjson.Valid(body) && gjson.Parse(body).IsObject() {
			asEvent := body
			if edited, ok := withUsageCost([]byte(body), edit); ok {
				asEvent = string(edited)
			}
			require.Equal(t, asEvent, whole)
		}
	})
}

// splitStreams are event streams in every framing the SSE specification
// allows, with a usage event whose upstream cost must not survive.
var splitStreams = []string{
	"data: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}\n\ndata: [DONE]\n\n",
	"data: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}\r\n\r\ndata: [DONE]\r\n\r\n",
	"data: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}\r\rdata: [DONE]\r\r",
	"data: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}\n\r\ndata: [DONE]\n\r\n",
	"event: x\rid: 1\rdata: {\"usage\":\rdata: {\"cost\":9}}\r\r: ping\r\rdata: [DONE]",
}

// However the writes split a stream, the client receives the same bytes, and
// never the upstream's cost.
func TestResponseCostWriter_EverySplitGivesTheSameStream(t *testing.T) {
	relayed := func(pieces ...string) string {
		w, rec := costWriterFor(t, http.Header{"Content-Type": {"text/event-stream"}})
		for _, piece := range pieces {
			_, err := w.Write([]byte(piece))
			require.NoError(t, err)
		}
		w.finish()
		return rec.Body.String()
	}
	for _, stream := range splitStreams {
		want := relayed(stream)
		assert.NotContains(t, want, `"cost":9`, "%q", stream)
		assert.Contains(t, want, `"cost":0.5`, "%q", stream)
		for i := 0; i <= len(stream); i++ {
			for j := i; j <= len(stream); j++ {
				require.Equal(t, want, relayed(stream[:i], stream[i:j], stream[j:]), "%q | %q | %q", stream[:i], stream[i:j], stream[j:])
			}
		}
	}
}
