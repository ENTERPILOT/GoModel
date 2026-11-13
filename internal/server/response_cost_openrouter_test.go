package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// An OpenRouter-style upstream reports its own cost in usage.cost, with a
// breakdown in usage.cost_details.
const (
	openRouterCost    = `"cost":0.0123`
	openRouterDetails = `"cost_details":{"upstream_inference_cost":0.0111}`
	openRouterUsage   = pricedUsage + `,` + openRouterCost + `,"is_byok":false,` + openRouterDetails
)

// openRouterRequests are the dialects an upstream cost can reach the client
// through, buffered and streamed.
func openRouterRequests() []costRequest {
	withUsage := func(body string) string {
		return strings.Replace(body, pricedUsage, openRouterUsage, 1)
	}
	anthropicMessage := strings.Replace(anthropicCostMessage, `"output_tokens":5}`, `"output_tokens":5,`+openRouterCost+`,`+openRouterDetails+`}`, 1)
	anthropicStream := strings.Replace(anthropicCostStream, `"usage":{"output_tokens":5}`, `"usage":{"output_tokens":5,`+openRouterCost+`,`+openRouterDetails+`}`, 1)
	return []costRequest{
		{
			name: "chat completions/buffered", path: "/v1/chat/completions", body: bufferedCostRequests[0].body,
			provider: func() *mockProvider {
				provider := costChatProvider()
				provider.response.Usage.RawUsage = map[string]any{
					"cost":         0.0123,
					"is_byok":      false,
					"cost_details": map[string]any{"upstream_inference_cost": 0.0111},
				}
				return provider
			},
		},
		{
			name: "chat completions/streamed", path: "/v1/chat/completions", body: streamedCostRequests[0].body,
			provider: func() *mockProvider {
				provider := costChatProvider()
				provider.streamData = strings.Replace(chatCostStream, `"total_tokens":15}`, `"total_tokens":15,`+openRouterCost+`,`+openRouterDetails+`}`, 1)
				return provider
			},
		},
		{
			name: "responses/streamed", path: "/v1/responses", body: streamedCostRequests[1].body,
			provider: func() *mockProvider {
				provider := costResponsesProvider()
				provider.streamData = strings.Replace(responsesCostStream, `"total_tokens":15}`, `"total_tokens":15,`+openRouterCost+`,`+openRouterDetails+`}`, 1)
				return provider
			},
		},
		{
			name: "messages/buffered", path: "/v1/messages", body: bufferedCostRequests[4].body,
			provider: costNativeMessagesProvider("application/json", anthropicMessage),
		},
		{
			name: "messages/streamed", path: "/v1/messages", body: streamedCostRequests[3].body,
			provider: costNativeMessagesProvider("text/event-stream", anthropicStream),
		},
		{
			name: "passthrough/buffered", path: passthroughChatPath, body: passthroughChatBody,
			provider: func() *mockProvider {
				return costPassthroughProvider(jsonHeader(), strings.NewReader(withUsage(`{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{`+pricedUsage+`}}`)))
			},
		},
		{
			name: "passthrough/streamed", path: passthroughChatPath, body: passthroughStreamBody,
			provider: func() *mockProvider {
				return costPassthroughProvider(sseHeader(), strings.NewReader(withUsage("data: {\"choices\":[],\"usage\":{"+pricedUsage+"}}\n\ndata: [DONE]\n\n")))
			},
		},
	}
}

// costMembers matches a cost member and one of its separators, so a body with
// costs removed can be compared byte for byte; costDetailsMembers likewise.
var (
	costMembers        = regexp.MustCompile(`,"cost":[0-9.]+|"cost":[0-9.]+,`)
	costDetailsMembers = regexp.MustCompile(`,"cost_details":\{[^{}]*\}|"cost_details":\{[^{}]*\},`)
)

// usagePayloads returns the payloads of a response that carry usage: the body
// of a buffered answer, or the data of each event with usage.
func usagePayloads(body string) [][]byte {
	if !strings.Contains(body, "data: ") {
		return [][]byte{[]byte(body)}
	}
	var payloads [][]byte
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if ok && (gjson.Get(data, "usage").IsObject() || gjson.Get(data, "response.usage").IsObject()) {
			payloads = append(payloads, []byte(data))
		}
	}
	return payloads
}

// With response costs on, an upstream's usage.cost never reaches the client:
// the cost is this gateway's, or absent when unknown. Its cost_details pass
// through unless markups are enabled. Nothing else in the response changes.
func TestResponseCost_OpenRouterUpstream(t *testing.T) {
	unpriced := func(*testing.T) func(*Config) {
		return func(cfg *Config) {
			cfg.ResponseCostEnabled = true
			cfg.PricingResolver = &mockPricingResolver{}
		}
	}
	modes := []struct {
		name        string
		configure   func(*testing.T) func(*Config)
		want        string // the cost the client reads; empty for none
		keepDetails bool
	}{
		{name: "markups off", configure: func(*testing.T) func(*Config) { return returnCosts }, want: responseCostAmount, keepDetails: true},
		{name: "markups on", configure: func(t *testing.T) func(*Config) { return billCosts(t, 100, true) }, want: "0.00004"},
		{name: "pricing unknown", configure: unpriced, keepDetails: true},
	}
	for _, tt := range openRouterRequests() {
		off, _ := costGateway(t, tt.provider())
		plain := postJSON(t, off, tt.path, tt.body).Body.String()
		require.Contains(t, plain, openRouterCost, "with costs off the upstream's cost passes through")
		require.Contains(t, plain, openRouterDetails)
		streamed := strings.HasSuffix(tt.name, "/streamed")

		for _, mode := range modes {
			t.Run(tt.name+"/"+mode.name, func(t *testing.T) {
				srv, _ := costGateway(t, tt.provider(), mode.configure(t))

				rec := postJSON(t, srv, tt.path, tt.body)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				body := rec.Body.String()
				assert.NotContains(t, body, openRouterCost)
				payloads := usagePayloads(body)
				require.NotEmpty(t, payloads, body)
				for _, payload := range payloads {
					assertOneGatewayCost(t, payload, mode.want)
					if !mode.keepDetails {
						assertNoCostDetails(t, payload)
					}
				}
				wantBody, gotBody := costMembers.ReplaceAllString(plain, ""), costMembers.ReplaceAllString(body, "")
				if mode.keepDetails {
					assert.Contains(t, body, openRouterDetails, "cost_details pass through exactly as received")
				} else {
					assert.NotContains(t, body, "cost_details")
					wantBody = costDetailsMembers.ReplaceAllString(wantBody, "")
				}
				assert.Equal(t, wantBody, gotBody, "only the cost members may differ")
				if streamed || mode.want == "" {
					assert.Empty(t, rec.Header().Values(responseCostHeader))
					return
				}
				assert.Equal(t, mode.want, rec.Header().Get(responseCostHeader), "the header and the body agree")
			})
		}
	}
}

// A returned cost is rounded to 12 decimal places and written as a plain
// decimal, in the body and the header alike.
func TestResponseCost_Rounding(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T) func(*Config)
		want      string
	}{
		{
			// 0.00002 * 1.5 is 0.000030000000000000004 in floating point.
			name:      "a marked-up cost carries no float noise",
			configure: func(t *testing.T) func(*Config) { return billCosts(t, 50, true) },
			want:      "0.00003",
		},
		{
			name: "a very small cost keeps its digits",
			configure: func(*testing.T) func(*Config) {
				return func(cfg *Config) {
					cfg.ResponseCostEnabled = true
					cfg.PricingResolver = &mockPricingResolver{pricing: &core.ModelPricing{InputPerMtok: new(0.0001), OutputPerMtok: new(0.0)}}
				}
			},
			want: "0.000000001",
		},
	}
	for _, tt := range tests {
		for _, request := range []costRequest{bufferedCostRequests[0], streamedCostRequests[0]} {
			streamed := strings.Contains(request.body, `"stream":true`)
			t.Run(tt.name+"/"+request.name+map[bool]string{false: "/buffered", true: "/streamed"}[streamed], func(t *testing.T) {
				srv, _ := costGateway(t, request.provider(), tt.configure(t))

				rec := postJSON(t, srv, request.path, request.body)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				cost, events := streamedCost(rec.Body.String())
				if streamed {
					require.Equal(t, 1, events, rec.Body.String())
				} else {
					cost = gjson.Get(rec.Body.String(), "usage.cost")
					assert.Equal(t, tt.want, rec.Header().Get(responseCostHeader))
				}
				assert.Equal(t, tt.want, cost.Raw)
			})
		}
	}
}

// Adding the cost to a relayed body changes nothing else: large integers,
// escapes and the member order stay as the upstream sent them.
func TestResponseCost_RelayedBodiesKeepEveryOtherByte(t *testing.T) {
	const head = `{"id":"chatcmpl-1","created":9007199254740993,"seed":123456789012345678901234567890,"text":"café \/ 😀","choices":[],"usage":{"total_tokens":15,"prompt_tokens":10,"completion_tokens":5,"reasoning":12345678901234567890`
	body := head + `}}`
	srv, _ := costGateway(t, costPassthroughProvider(jsonHeader(), strings.NewReader(body)), returnCosts)

	rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, head+costMember(responseCostAmount)+`}}`, rec.Body.String())
}
