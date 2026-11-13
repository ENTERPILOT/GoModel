package server

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/usage"
)

func TestWithUsageCost(t *testing.T) {
	const cost = `"cost":0.00000542`
	tests := []struct {
		name    string
		data    string
		unknown bool
		billed  bool
		want    string // empty: the payload is left as it is
	}{
		{
			name: "chat usage chunk",
			data: `{"choices":[],"usage":{"prompt_tokens":271,"total_tokens":271}}`,
			want: `{"choices":[],"usage":{"prompt_tokens":271,"total_tokens":271,` + cost + `}}`,
		},
		{
			name: "anthropic message_delta",
			data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			want: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,` + cost + `}}`,
		},
		{
			name: "responses terminal event",
			data: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1},"status":"completed"}}`,
			want: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,` + cost + `},"status":"completed"}}`,
		},
		{
			name: "an upstream cost is replaced, its details kept",
			data: `{"usage":{"total_tokens":3,"cost":0.5,"cost_details":{"upstream_inference_cost":0.4}}}`,
			want: `{"usage":{"total_tokens":3,"cost_details":{"upstream_inference_cost":0.4},` + cost + `}}`,
		},
		{
			name:   "billed: the upstream's cost and details are removed",
			data:   `{"usage":{"total_tokens":3,"cost":0.5,"cost_details":{"upstream_inference_cost":0.4}}}`,
			billed: true,
			want:   `{"usage":{"total_tokens":3,` + cost + `}}`,
		},
		{
			name:    "billed without a cost: details are removed all the same",
			data:    `{"usage":{"cost_details":{},"total_tokens":3,"Cost_Details":null,"cost\u005fdetails":1}}`,
			unknown: true,
			billed:  true,
			want:    `{"usage":{"total_tokens":3}}`,
		},
		{
			name: "empty usage object",
			data: `{"usage": { }}`,
			want: `{"usage": { ` + cost + `}}`,
		},
		{
			name: "formatted payload",
			data: "{\n  \"usage\": {\n    \"total_tokens\": 3,\n    \"cost\": 9\n  }\n}",
			want: "{\n  \"usage\": {\n    \"total_tokens\": 3," + cost + "\n  }\n}",
		},
		{
			name:    "an upstream cost is removed when this gateway has none",
			data:    `{"usage":{"total_tokens":3,"cost":9}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate members: the last one is the upstream's",
			data: `{"usage":{"cost":null,"total_tokens":3,"cost":9}}`,
			want: `{"usage":{"total_tokens":3,` + cost + `}}`,
		},
		{
			name:    "duplicate members without a cost",
			data:    `{"usage":{"cost":null,"total_tokens":3,"cost":9}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate usage objects each carry the cost",
			data: `{"usage":{"total_tokens":3},"choices":[],"usage":{"total_tokens":3,"cost":9}}`,
			want: `{"usage":{"total_tokens":3,` + cost + `},"choices":[],"usage":{"total_tokens":3,` + cost + `}}`,
		},
		{
			name:    "duplicate usage objects without a cost",
			data:    `{"usage":{"total_tokens":3},"usage":{"total_tokens":3,"cost":9}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3},"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate response objects",
			data: `{"type":"response.completed","response":{"usage":{"input_tokens":1}},"response":{"usage":{"input_tokens":1,"cost":9}}}`,
			want: `{"type":"response.completed","response":{"usage":{"input_tokens":1,` + cost + `}},"response":{"usage":{"input_tokens":1,` + cost + `}}}`,
		},
		{
			// Go matches struct fields case-insensitively, folding ſ to s, and
			// every parser unescapes member names.
			name: "differently spelled names are removed",
			data: `{"usage":{"total_tokens":3,"COST":7,"co\u017ft":8,"c\u006fst":9},"USAGE":{"Cost":6}}`,
			want: `{"usage":{"total_tokens":3,` + cost + `},"USAGE":{}}`,
		},
		{
			name: "an escaped usage name carries the cost",
			data: `{"us\u0061ge":{"total_tokens":3}}`,
			want: `{"us\u0061ge":{"total_tokens":3,` + cost + `}}`,
		},
		{
			name:    "message_start usage only loses an upstream's member",
			data:    `{"type":"message_start","message":{"usage":{"input_tokens":9,"cost":9}}}`,
			unknown: true,
			want:    `{"type":"message_start","message":{"usage":{"input_tokens":9}}}`,
		},
		{
			name: "leading whitespace",
			data: `  {"usage":{"cost":1,"total_tokens":3}}`,
			want: `  {"usage":{"total_tokens":3,` + cost + `}}`,
		},
		{
			name: "large integers keep every digit",
			data: `{"seed":123456789012345678901234567890,"usage":{"total_tokens":9007199254740993,"cost":1e-3}}`,
			want: `{"seed":123456789012345678901234567890,"usage":{"total_tokens":9007199254740993,` + cost + `}}`,
		},
		{name: "unknown cost adds nothing", data: `{"usage":{"total_tokens":3,"cost_details":{}}}`, unknown: true},
		{name: "usage null", data: `{"choices":[],"usage":null}`},
		{name: "usage nested elsewhere", data: `{"type":"message_start","message":{"usage":{"input_tokens":9}}}`},
		{name: "response without usage", data: `{"type":"response.created","response":{"id":"resp_1","usage":null}}`},
		{name: "invalid JSON", data: `{"usage":{"total_tokens":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := withUsageCost([]byte(tt.data), newUsageCostEdit(0.00000542, !tt.unknown, tt.billed))

			if tt.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, string(got))
			assert.True(t, gjson.ValidBytes(got), "the rewritten payload must stay valid JSON")
			wantCost := "0.00000542"
			if tt.unknown {
				wantCost = ""
			}
			assertOneGatewayCost(t, got, wantCost)
			if tt.billed {
				assertNoCostDetails(t, got)
			}
		})
	}
}

// decodedUsages decodes payload the way a Go, JavaScript or Python client
// does, keeping the last of duplicate members, and returns the usage objects
// it reads: at the top level and under "response".
func decodedUsages(t *testing.T, payload []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded map[string]any
	require.NoError(t, decoder.Decode(&decoded))
	var usages []map[string]any
	if object, ok := decoded["usage"].(map[string]any); ok {
		usages = append(usages, object)
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if object, ok := response["usage"].(map[string]any); ok {
			usages = append(usages, object)
		}
	}
	return usages
}

// typedUsage is what a struct decoder, which matches member names
// case-insensitively, reads from a payload.
type typedUsage struct {
	Usage struct {
		Cost        *json.Number    `json:"cost"`
		CostDetails json.RawMessage `json:"cost_details"`
	} `json:"usage"`
}

// assertOneGatewayCost checks that whichever way a client decodes payload, the
// usage it reads holds this gateway's cost, want, as a plain number, or no
// cost at all when want is empty.
func assertOneGatewayCost(t *testing.T, payload []byte, want string) {
	t.Helper()
	for _, object := range decodedUsages(t, payload) {
		cost, ok := object[usageCostMember]
		if want == "" {
			assert.False(t, ok, "no cost member may survive: %s", payload)
			continue
		}
		assert.Equal(t, json.Number(want), cost, "%s", payload)
	}
	var typed typedUsage
	require.NoError(t, json.Unmarshal(payload, &typed))
	if typed.Usage.Cost != nil {
		require.NotEmpty(t, want, "no cost member may survive: %s", payload)
		assert.Equal(t, want, typed.Usage.Cost.String())
	}
}

// assertNoCostDetails checks that no decoder finds a cost_details member.
func assertNoCostDetails(t *testing.T, payload []byte) {
	t.Helper()
	for _, object := range decodedUsages(t, payload) {
		assert.NotContains(t, object, "cost_details", "%s", payload)
	}
	var typed typedUsage
	require.NoError(t, json.Unmarshal(payload, &typed))
	assert.Empty(t, typed.Usage.CostDetails, "%s", payload)
}

func TestFormatResponseCost(t *testing.T) {
	cost, markup := 0.00002, 1.5
	require.NotEqual(t, 0.00003, cost*markup, "the product carries float noise")
	tests := []struct {
		name   string
		amount float64
		want   string
	}{
		{name: "zero", amount: 0, want: "0"},
		{name: "negative zero", amount: math.Copysign(0, -1), want: "0"},
		{name: "plain", amount: 0.00000542, want: "0.00000542"},
		{name: "float noise is rounded away", amount: cost * markup, want: "0.00003"},
		{name: "very small", amount: 1e-9, want: "0.000000001"},
		{name: "all twelve places", amount: 0.123456789012, want: "0.123456789012"},
		{name: "rounded at the twelfth place", amount: 0.0000000000016, want: "0.000000000002"},
		{name: "below the twelfth place", amount: 4e-13, want: "0"},
		{name: "whole", amount: 12, want: "12"},
		{name: "large", amount: 1e21, want: "1000000000000000000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatResponseCost(tt.amount)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "e", "never in exponent notation")
			assert.True(t, gjson.Valid(got), "a JSON number")
		})
	}
}

// costWriterFor returns a writer whose request cost $0.5.
func costWriterFor(t *testing.T, header http.Header) (*responseCostWriter, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, report := usage.WithCostReport(t.Context(), false)
	usage.ReportCost(ctx, nil, &usage.UsageEntry{TotalCost: new(0.5)})
	rec := httptest.NewRecorder()
	maps.Copy(rec.Header(), header)
	return &responseCostWriter{ResponseWriter: rec, report: report, editsJSON: true}, rec
}

// Events reach the writer in arbitrary pieces; the usage event is rewritten
// wherever the pieces split it, and an unterminated last event is not lost.
func TestResponseCostWriter_RewritesEventsSplitAcrossWrites(t *testing.T) {
	const stream = "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}],\"usage\":null}\n\n" +
		": keep-alive\n\n" +
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\n" +
		"data: [DONE]"
	const want = "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}],\"usage\":null}\n\n" +
		": keep-alive\n\n" +
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":3,\"cost\":0.5}}\n\n" +
		"data: [DONE]"

	for _, size := range []int{1, 7, 64, len(stream)} {
		w, rec := costWriterFor(t, http.Header{"Content-Type": {"text/event-stream"}})
		for rest := stream; len(rest) > 0; {
			n := min(size, len(rest))
			written, err := w.Write([]byte(rest[:n]))
			require.NoError(t, err)
			require.Equal(t, n, written)
			rest = rest[n:]
		}
		w.finish()

		assert.Equal(t, want, rec.Body.String(), "write size %d", size)
	}
}

func TestResponseCostWriter_LeavesOtherBodiesAlone(t *testing.T) {
	const body = `{"usage":{"total_tokens":3,"cost":9}}`
	tests := []struct {
		name       string
		header     http.Header
		wantHeader string
	}{
		{name: "not JSON", header: http.Header{"Content-Type": {"text/plain"}}, wantHeader: "0.5"},
		{name: "encoded JSON", header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, wantHeader: "0.5"},
		{name: "encoded event stream", header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := costWriterFor(t, tt.header)

			_, err := w.Write([]byte(body))
			require.NoError(t, err)
			w.finish()

			assert.Equal(t, body, rec.Body.String())
			assert.Equal(t, tt.wantHeader, rec.Header().Get(responseCostHeader))
		})
	}
}

// Header.Del removes only the canonical key; a cost header an upstream or a
// handler set under another spelling, or more than once, must not survive.
func TestResponseCostWriter_ReplacesEveryCostHeader(t *testing.T) {
	upstream := http.Header{
		"X-Gomodel-Cost":           {"9", "8"},
		"x-gomodel-cost":           {"7"},
		"X-GOMODEL-COST ":          {"6"},
		"x-gomodel-cost-currency":  {"EUR"},
		"X-Gomodel-Cost-Currency":  {"EUR", "PLN"},
		"X-GoModel-Cost-Unrelated": {"kept"},
	}
	tests := []struct {
		name    string
		unknown bool
		want    http.Header
	}{
		{name: "known cost", want: http.Header{
			"X-Gomodel-Cost":           {"0.5"},
			"X-Gomodel-Cost-Currency":  {"USD"},
			"X-GoModel-Cost-Unrelated": {"kept"},
		}},
		{name: "unknown cost", unknown: true, want: http.Header{"X-GoModel-Cost-Unrelated": {"kept"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := costWriterFor(t, upstream)
			if tt.unknown {
				_, w.report = usage.WithCostReport(t.Context(), false)
			}

			w.WriteHeader(http.StatusOK)

			assert.Equal(t, tt.want, rec.Header())
		})
	}
}

func TestMayHoldCost(t *testing.T) {
	tests := []struct {
		data   string
		want   bool
		billed bool // what a billed edit reads on top
	}{
		{data: `{"usage":{"total_tokens":3}}`, want: true, billed: true},
		{data: `{"USAGE":{}}`, want: true, billed: true},
		{data: `{"uſage":{}}`, want: true, billed: true},
		{data: `{"\u0075sage":{}}`, want: true, billed: true},
		{data: `{"choices":[],"cost":0.0009}`, billed: true},
		{data: `{"choices":[],"COST":0.0009}`, billed: true},
		{data: `{"choices":[{"delta":{"content":"Hi"}}]}`},
		{data: `[DONE]`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, mayHoldCost([]byte(tt.data), false), tt.data)
		assert.Equal(t, tt.billed, mayHoldCost([]byte(tt.data), true), "billed: %s", tt.data)
	}
}
