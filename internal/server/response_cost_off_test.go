package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offResponse is what a client receives, minus the per-request id.
type offResponse struct {
	code   int
	header http.Header
	body   string
}

func receivedResponse(rec *httptest.ResponseRecorder) offResponse {
	header := rec.Header().Clone()
	header.Del("X-Request-Id")
	return offResponse{code: rec.Code, header: header, body: rec.Body.String()}
}

// With response costs off, every response is byte for byte the one a gateway
// without any of the feature's code sends. The baseline runs without usage
// tracking, so nothing reports a cost, the cache records no hit usage, and
// passthrough takes its plain relay; passthrough answers are also compared
// with the upstream's own bytes, its usage.cost, cost_details and cost header
// included.
func TestResponseCost_OffChangesNoByte(t *testing.T) {
	const upstreamJSON = `{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{` + openRouterUsage + `,"COST":7,"cost":9}}`
	const upstreamStream = "data: {\"choices\":[],\"usage\":{" + openRouterUsage + ",\"Cost\":9}}\r\n\r\ndata: [DONE]\r\n\r\n"
	upstreamHeader := func(contentType string) http.Header {
		return http.Header{"Content-Type": {contentType}, "X-Gomodel-Cost": {"9"}, "X-Gomodel-Cost-Currency": {"USD"}}
	}
	type offCase struct {
		name     string
		provider func() *mockProvider
		path     string
		body     string
		cached   bool
		upstream string // the upstream's body, for a relayed answer
	}
	tests := []offCase{
		{name: "buffered chat", provider: costChatProvider, path: "/v1/chat/completions", body: bufferedCostRequests[0].body},
		{name: "streamed chat", provider: costChatProvider, path: "/v1/chat/completions", body: streamedCostRequests[0].body},
		{name: "buffered embeddings", provider: costEmbeddingsProvider, path: "/v1/embeddings", body: bufferedCostRequests[2].body},
		{name: "cache hit", provider: costChatProvider, path: "/v1/chat/completions", body: bufferedCostRequests[0].body, cached: true},
		{name: "streamed cache hit", provider: costChatProvider, path: "/v1/chat/completions", body: streamedCostRequests[0].body, cached: true},
		{
			name: "passthrough JSON", path: passthroughChatPath, body: passthroughChatBody, upstream: upstreamJSON,
			provider: func() *mockProvider {
				return costPassthroughProvider(upstreamHeader("application/json"), strings.NewReader(upstreamJSON))
			},
		},
		{
			name: "passthrough stream", path: passthroughChatPath, body: passthroughStreamBody, upstream: upstreamStream,
			provider: func() *mockProvider {
				return costPassthroughProvider(upstreamHeader("text/event-stream"), strings.NewReader(upstreamStream))
			},
		},
	}
	// Every dialect with an upstream's own usage.cost and cost_details.
	for _, request := range openRouterRequests() {
		tests = append(tests, offCase{name: "openrouter " + request.name, provider: request.provider, path: request.path, body: request.body})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			send := func(configure ...func(*Config)) offResponse {
				provider := tt.provider()
				if tt.cached {
					configure = append(configure, cachedResponses(t))
				}
				srv, _ := costGateway(t, provider, configure...)
				rec := postJSON(t, srv, tt.path, tt.body)
				if tt.cached {
					require.Eventually(t, func() bool {
						rec = postJSON(t, srv, tt.path, tt.body)
						return rec.Header().Get("X-Cache") != ""
					}, 2*time.Second, 5*time.Millisecond)
				}
				return receivedResponse(rec)
			}
			bypassed := send(func(cfg *Config) { cfg.UsageLogger = nil })

			off := send()

			require.Equal(t, http.StatusOK, off.code, off.body)
			assert.Equal(t, bypassed, off)
			if tt.upstream != "" {
				assert.Equal(t, tt.upstream, off.body)
				assert.Equal(t, []string{"9"}, off.header.Values(responseCostHeader), "with costs off an upstream's header is relayed")
			}
		})
	}
}
