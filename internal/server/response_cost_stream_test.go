package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/usage"
)

func TestWithUsageCost(t *testing.T) {
	const cost = `{"amount":0.00000542,"currency":"USD"}`
	tests := []struct {
		name    string
		data    string
		unknown bool
		want    string // empty: the payload is left as it is
	}{
		{
			name: "chat usage chunk",
			data: `{"choices":[],"usage":{"prompt_tokens":271,"total_tokens":271}}`,
			want: `{"choices":[],"usage":{"prompt_tokens":271,"total_tokens":271,"gomodel_cost":` + cost + `}}`,
		},
		{
			name: "anthropic message_delta",
			data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			want: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"gomodel_cost":` + cost + `}}`,
		},
		{
			name: "responses terminal event",
			data: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1},"status":"completed"}}`,
			want: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"gomodel_cost":` + cost + `},"status":"completed"}}`,
		},
		{
			name: "provider cost fields stay",
			data: `{"usage":{"total_tokens":3,"cost":0.5,"cost_details":{"upstream_inference_cost":0.4}}}`,
			want: `{"usage":{"total_tokens":3,"cost":0.5,"cost_details":{"upstream_inference_cost":0.4},"gomodel_cost":` + cost + `}}`,
		},
		{
			name: "empty usage object",
			data: `{"usage": { }}`,
			want: `{"usage": { "gomodel_cost":` + cost + `}}`,
		},
		{
			name: "formatted payload",
			data: "{\n  \"usage\": {\n    \"total_tokens\": 3\n  }\n}",
			want: "{\n  \"usage\": {\n    \"total_tokens\": 3\n  ,\"gomodel_cost\":" + cost + "}\n}",
		},
		{
			name: "an upstream cost is replaced",
			data: `{"usage":{"gomodel_cost":{"amount":9,"currency":"USD"},"total_tokens":3}}`,
			want: `{"usage":{"total_tokens":3,"gomodel_cost":` + cost + `}}`,
		},
		{
			name:    "an upstream cost is removed when this gateway has none",
			data:    `{"usage":{"total_tokens":3,"gomodel_cost":{"amount":9,"currency":"USD"}}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate members: the last one is the upstream's",
			data: `{"usage":{"gomodel_cost":null,"total_tokens":3,"gomodel_cost":{"amount":9,"currency":"USD"}}}`,
			want: `{"usage":{"total_tokens":3,"gomodel_cost":` + cost + `}}`,
		},
		{
			name:    "duplicate members without a cost",
			data:    `{"usage":{"gomodel_cost":null,"total_tokens":3,"gomodel_cost":{"amount":9,"currency":"USD"}}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate usage objects each carry the cost",
			data: `{"usage":{"total_tokens":3},"choices":[],"usage":{"total_tokens":3,"gomodel_cost":{"amount":9,"currency":"USD"}}}`,
			want: `{"usage":{"total_tokens":3,"gomodel_cost":` + cost + `},"choices":[],"usage":{"total_tokens":3,"gomodel_cost":` + cost + `}}`,
		},
		{
			name:    "duplicate usage objects without a cost",
			data:    `{"usage":{"total_tokens":3},"usage":{"total_tokens":3,"gomodel_cost":{"amount":9,"currency":"USD"}}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":3},"usage":{"total_tokens":3}}`,
		},
		{
			name: "duplicate response objects",
			data: `{"type":"response.completed","response":{"usage":{"input_tokens":1}},"response":{"usage":{"input_tokens":1,"gomodel_cost":{"amount":9}}}}`,
			want: `{"type":"response.completed","response":{"usage":{"input_tokens":1,"gomodel_cost":` + cost + `}},"response":{"usage":{"input_tokens":1,"gomodel_cost":` + cost + `}}}`,
		},
		{
			// Go matches struct fields case-insensitively, folding ſ to s, and
			// every parser unescapes member names.
			name: "differently spelled names are removed",
			data: `{"usage":{"total_tokens":3,"GoModel_Cost":{"amount":7},"gomodel_co\u017ft":{"amount":8},"gomodel\u005fcost":{"amount":9}},"USAGE":{"gomodel_cost":{"amount":6}}}`,
			want: `{"usage":{"total_tokens":3,"gomodel_cost":` + cost + `},"USAGE":{}}`,
		},
		{
			name: "an escaped usage name carries the cost",
			data: `{"us\u0061ge":{"total_tokens":3}}`,
			want: `{"us\u0061ge":{"total_tokens":3,"gomodel_cost":` + cost + `}}`,
		},
		{
			name:    "message_start usage only loses an upstream's member",
			data:    `{"type":"message_start","message":{"usage":{"input_tokens":9,"gomodel_cost":{"amount":9}}}}`,
			unknown: true,
			want:    `{"type":"message_start","message":{"usage":{"input_tokens":9}}}`,
		},
		{
			name: "leading whitespace",
			data: `  {"usage":{"gomodel_cost":1,"total_tokens":3}}`,
			want: `  {"usage":{"total_tokens":3,"gomodel_cost":` + cost + `}}`,
		},
		{name: "unknown cost adds nothing", data: `{"usage":{"total_tokens":3}}`, unknown: true},
		{name: "usage null", data: `{"choices":[],"usage":null}`},
		{name: "usage nested elsewhere", data: `{"type":"message_start","message":{"usage":{"input_tokens":9}}}`},
		{name: "response without usage", data: `{"type":"response.created","response":{"id":"resp_1","usage":null}}`},
		{name: "invalid JSON", data: `{"usage":{"total_tokens":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := withUsageCost([]byte(tt.data), 0.00000542, !tt.unknown)

			if tt.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, string(got))
			assert.True(t, gjson.ValidBytes(got), "the rewritten payload must stay valid JSON")
			assertOneGatewayCost(t, got, 0.00000542, !tt.unknown)
		})
	}
}

// assertOneGatewayCost decodes payload the way a Go, JavaScript or Python
// client does, keeping the last of duplicate members, and checks that the
// usage it reads holds this gateway's cost exactly once, or none.
func assertOneGatewayCost(t *testing.T, payload []byte, amount float64, known bool) {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	usages := []any{decoded["usage"]}
	if response, ok := decoded["response"].(map[string]any); ok {
		usages = append(usages, response["usage"])
	}
	for _, found := range usages {
		object, ok := found.(map[string]any)
		if !ok {
			continue
		}
		cost, ok := object[responseCostMember]
		if !known {
			assert.False(t, ok, "no cost member may survive: %s", payload)
			continue
		}
		assert.Equal(t, map[string]any{"amount": amount, "currency": "USD"}, cost)
	}
	// A struct decoder matches member names case-insensitively.
	var typed struct {
		Usage struct {
			Cost *struct {
				Amount float64 `json:"amount"`
			} `json:"gomodel_cost"`
		} `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(payload, &typed))
	if typed.Usage.Cost != nil {
		assert.True(t, known, "no cost member may survive: %s", payload)
		assert.Equal(t, amount, typed.Usage.Cost.Amount)
	}
}

func TestFormatResponseCost(t *testing.T) {
	tests := []struct {
		amount float64
		want   string
	}{
		{amount: 0, want: "0"},
		{amount: 0.00000542, want: "0.00000542"},
		{amount: 1e-12, want: "0.000000000001"},
		{amount: 12.5, want: "12.5"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatResponseCost(tt.amount))
	}
	// No digit is rounded away: the value is the one the usage record holds.
	cost, markup := 0.00002, 1.5
	assert.Equal(t, "0.000030000000000000004", formatResponseCost(cost*markup))
}

// costWriterFor returns a writer whose request cost $0.5.
func costWriterFor(t *testing.T, header http.Header) (*responseCostWriter, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, report := usage.WithCostReport(t.Context(), false)
	usage.ReportCost(ctx, nil, &usage.UsageEntry{TotalCost: new(0.5)})
	rec := httptest.NewRecorder()
	maps.Copy(rec.Header(), header)
	return &responseCostWriter{ResponseWriter: rec, report: report}, rec
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
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":3,\"gomodel_cost\":{\"amount\":0.5,\"currency\":\"USD\"}}}\n\n" +
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
	const body = "data: {\"usage\":{\"total_tokens\":3}}\n\n"
	tests := []struct {
		name   string
		header http.Header
	}{
		{name: "JSON", header: http.Header{"Content-Type": {"application/json"}}},
		{name: "encoded event stream", header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := costWriterFor(t, tt.header)

			_, err := w.Write([]byte(body))
			require.NoError(t, err)
			w.finish()

			assert.Equal(t, body, rec.Body.String())
			assert.Equal(t, "0.5", rec.Header().Get(responseCostHeader))
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

func TestMayHoldUsage(t *testing.T) {
	tests := []struct {
		data string
		want bool
	}{
		{data: `{"usage":{"total_tokens":3}}`, want: true},
		{data: `{"USAGE":{}}`, want: true},
		{data: `{"uſage":{}}`, want: true},
		{data: `{"usage":{}}`, want: true},
		{data: `{"choices":[{"delta":{"content":"Hi"}}]}`},
		{data: `[DONE]`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, mayHoldUsage([]byte(tt.data)), tt.data)
	}
}
