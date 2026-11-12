package server

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	passthroughChatPath   = "/p/openai/chat/completions"
	passthroughChatBody   = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`
	passthroughStreamBody = `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	// pricedUsage is the usage every fixture reports: a provider cost of
	// responseCostAmount.
	pricedUsage = `"prompt_tokens":10,"completion_tokens":5,"total_tokens":15`
)

// costPassthroughProvider answers /p/openai/... with body under header. A
// body that can be closed is handed over as it is, so a test sees the close.
func costPassthroughProvider(header http.Header, body io.Reader) *mockProvider {
	closer, ok := body.(io.ReadCloser)
	if !ok {
		closer = io.NopCloser(body)
	}
	return &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    header,
			Body:       closer,
		},
	}
}

func sseHeader() http.Header  { return http.Header{"Content-Type": {"text/event-stream"}} }
func jsonHeader() http.Header { return http.Header{"Content-Type": {"application/json"}} }

// usageEventData returns the payload of the one event that carries usage.
func usageEventData(t *testing.T, body string) []byte {
	t.Helper()
	var found []string
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), "data: ")
		if ok && strings.Contains(data, "tokens") {
			found = append(found, data)
		}
	}
	require.Len(t, found, 1, body)
	return []byte(found[0])
}

// Duplicate members and objects are read differently by different parsers
// (Go, JavaScript and Python keep the last one). Whichever a client keeps,
// it reads this gateway's cost or none, and with markups no cost_details.
func TestResponseCost_UpstreamDuplicatesNeverReachTheClient(t *testing.T) {
	const upstreamCost = `"cost":9,"cost_details":{"upstream_inference_cost":8}`
	payloads := []struct {
		name    string
		payload string
	}{
		{name: "duplicate members", payload: `{"choices":[],"usage":{"cost":null,"cost_details":null,` + pricedUsage + `,` + upstreamCost + `}}`},
		{name: "duplicate usage objects", payload: `{"usage":{` + pricedUsage + `},"choices":[],"usage":{` + pricedUsage + `,` + upstreamCost + `}}`},
		{name: "cased members", payload: `{"choices":[],"usage":{` + pricedUsage + `,"COST":9,"Cost_Details":{"upstream_inference_cost":8}}}`},
		{name: "escaped members", payload: `{"choices":[],"usage":{` + pricedUsage + `,"c\u006fst":9,"co\u017ft_details":{"upstream_inference_cost":8}}}`},
	}
	modes := []struct {
		name      string
		configure func(*testing.T) func(*Config)
		want      string
	}{
		{name: "priced", configure: func(*testing.T) func(*Config) { return returnCosts }, want: responseCostAmount},
		{name: "billed", configure: func(t *testing.T) func(*Config) { return billCosts(t, 100, true) }, want: "0.00004"},
		{name: "without pricing", configure: func(*testing.T) func(*Config) {
			return func(cfg *Config) {
				cfg.ResponseCostEnabled = true
				cfg.PricingResolver = &mockPricingResolver{}
			}
		}},
	}
	for _, p := range payloads {
		for _, mode := range modes {
			t.Run(p.name+"/"+mode.name+"/streamed", func(t *testing.T) {
				stream := "data: " + p.payload + "\n\ndata: [DONE]\n\n"
				srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), strings.NewReader(stream)), mode.configure(t))

				rec := postJSON(t, srv, passthroughChatPath, passthroughStreamBody)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				data := usageEventData(t, rec.Body.String())
				assertOneGatewayCost(t, data, mode.want)
				if mode.name == "billed" {
					assertNoCostDetails(t, data)
				}
				assert.NotContains(t, string(data), ":9")
			})

			t.Run(p.name+"/"+mode.name+"/buffered", func(t *testing.T) {
				header := jsonHeader()
				header["x-gomodel-cost"] = []string{"9"}
				header["X-Gomodel-Cost"] = []string{"8", "7"}
				header["X-GOMODEL-COST-CURRENCY"] = []string{"EUR"}
				srv, _ := costGateway(t, costPassthroughProvider(header, strings.NewReader(p.payload)), mode.configure(t))

				rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assertOneGatewayCost(t, rec.Body.Bytes(), mode.want)
				if mode.name == "billed" {
					assertNoCostDetails(t, rec.Body.Bytes())
				}
				assert.NotContains(t, rec.Body.String(), ":9")
				if mode.want == "" {
					assert.Empty(t, rec.Header().Values(responseCostHeader))
					assert.Empty(t, rec.Header().Values(responseCostCurrencyHeader))
				} else {
					assert.Equal(t, []string{mode.want}, rec.Header().Values(responseCostHeader))
					assert.Equal(t, []string{"USD"}, rec.Header().Values(responseCostCurrencyHeader))
				}
				for key := range rec.Header() {
					if strings.EqualFold(key, responseCostHeader) || strings.EqualFold(key, responseCostCurrencyHeader) {
						assert.Contains(t, []string{"X-Gomodel-Cost", "X-Gomodel-Cost-Currency"}, key)
					}
				}
			})
		}
	}
}

// The gateway asks upstreams for no encoding and the transport decodes gzip
// itself, so an encoded body is one the upstream sent unasked. It cannot be
// read for usage or cleared of an upstream's cost, so with costs returned it
// is refused instead of relayed as if this gateway vouched for it.
func TestResponseCost_RefusesEncodedAnswers(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		path   string
		body   string
	}{
		{name: "stream", header: sseHeader(), body: passthroughStreamBody},
		{name: "JSON", header: jsonHeader(), body: passthroughChatBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const encoded = "\x1b\x00encoded-bytes"
			newProvider := func() *mockProvider {
				header := tt.header.Clone()
				header.Set("Content-Encoding", "br")
				return costPassthroughProvider(header, strings.NewReader(encoded))
			}
			off, _ := costGateway(t, newProvider())
			on, _ := costGateway(t, newProvider(), returnCosts)

			relayed := postJSON(t, off, passthroughChatPath, tt.body)
			refused := postJSON(t, on, passthroughChatPath, tt.body)

			assert.Equal(t, http.StatusOK, relayed.Code)
			assert.Equal(t, encoded, relayed.Body.String(), "with costs off the answer is relayed as it is")
			assert.Equal(t, http.StatusBadGateway, refused.Code, refused.Body.String())
			assert.NotContains(t, refused.Body.String(), "encoded-bytes")
			assert.Empty(t, refused.Header().Values(responseCostHeader))
		})
	}

	t.Run("identity is not an encoding", func(t *testing.T) {
		header := jsonHeader()
		header.Set("Content-Encoding", "identity")
		body := `{"id":"chatcmpl-1","choices":[],"usage":{` + pricedUsage + `}}`
		srv, _ := costGateway(t, costPassthroughProvider(header, strings.NewReader(body)), returnCosts)

		rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, responseCostAmount, rec.Header().Get(responseCostHeader))
	})
}

// paddedChatAnswer returns a chat answer of exactly size bytes, with an
// upstream's cost in its usage.
func paddedChatAnswer(t *testing.T, size int) string {
	t.Helper()
	head := `{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{` + pricedUsage + `,"cost":9},"padding":"`
	tail := `"}`
	require.Greater(t, size, len(head)+len(tail))
	return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
}

// A relayed JSON answer is read whole, up to 8 MiB, before it is sent, so it
// can carry its cost. A larger answer is relayed as it arrives, unpriced: it
// carries no cost header and no cost, and loses the upstream's.
func TestResponseCost_RelayedJSONSizeLimit(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		wantCost string
	}{
		{name: "at the limit", size: maxObservedJSONResponseBytes, wantCost: responseCostAmount},
		{name: "one byte over", size: maxObservedJSONResponseBytes + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := paddedChatAnswer(t, tt.size)
			srv, logger := costGateway(t, costPassthroughProvider(jsonHeader(), strings.NewReader(body)), returnCosts)

			rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantCost, rec.Header().Get(responseCostHeader))
			if tt.wantCost == "" {
				assert.Empty(t, logger.entries, "an answer too large to read is not accounted for")
				assert.Equal(t, strings.Replace(body, `,"cost":9`, "", 1), rec.Body.String())
				return
			}
			require.Len(t, logger.entries, 1)
			assert.Equal(t, strings.Replace(body, `,"cost":9`, costMember(tt.wantCost), 1), rec.Body.String())
		})
	}
}

// A stream framed with CRLF keeps its framing: the rewritten usage event ends
// its lines with CRLF like every other event, so a client that splits on
// "\r\n\r\n" still finds it.
func TestResponseCost_CRLFStreams(t *testing.T) {
	const stream = "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\r\n\r\n" +
		": keep-alive\r\n\r\n" +
		"event: chunk\r\nid: 7\r\ndata: {\"choices\":[],\"usage\":{" + pricedUsage + "}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	want := strings.Replace(stream, pricedUsage+"}", pricedUsage+costMember(responseCostAmount)+"}", 1)

	srv, logger := costGateway(t, costPassthroughProvider(sseHeader(), strings.NewReader(stream)), returnCosts)

	rec := postJSON(t, srv, passthroughChatPath, passthroughStreamBody)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, want, rec.Body.String())
	require.Len(t, logger.entries, 1)
	assert.Equal(t, responseCostAmount, formatResponseCost(*logger.entries[0].TotalCost))
}

// A provider that reports usage on every chunk gets the cost on every such
// event. Each carries the cost of the usage so far, cumulative like the token
// counts beside it, and the last one is the cost the usage log records.
func TestResponseCost_UsageOnEveryChunk(t *testing.T) {
	chunk := func(delta, usage string) string {
		return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[` + delta + `],"usage":{` + usage + "}}\n\n"
	}
	provider := costChatProvider()
	provider.streamData = chunk(`{"index":0,"delta":{"content":"Hi"},"finish_reason":null}`, `"prompt_tokens":10,"completion_tokens":1,"total_tokens":11`) +
		chunk(`{"index":0,"delta":{"content":"!"},"finish_reason":null}`, `"prompt_tokens":10,"completion_tokens":3,"total_tokens":13`) +
		chunk(`{"index":0,"delta":{},"finish_reason":"stop"}`, pricedUsage) +
		"data: [DONE]\n\n"
	srv, logger := costGateway(t, provider, returnCosts)

	rec := postJSON(t, srv, "/v1/chat/completions", streamedCostRequests[0].body)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var amounts []gjson.Result
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "usage").Exists() {
			amounts = append(amounts, gjson.Get(data, "usage.cost"))
		}
	}
	require.Len(t, amounts, 3, rec.Body.String())
	for i, want := range []string{"0.000012", "0.000016", responseCostAmount} {
		assert.Equal(t, want, amounts[i].Raw, "event %d carries the cost so far", i)
	}
	require.Len(t, logger.entries, 1)
	assert.Equal(t, formatResponseCost(*logger.entries[0].TotalCost), amounts[2].Raw, "the last cost is the logged one")
}

// A client that goes away mid-stream ends the relay: the gateway stops
// writing and closes the upstream body instead of draining it.
func TestResponseCost_ClientDisconnectMidStream(t *testing.T) {
	upstream, feed := io.Pipe()
	srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), upstream), returnCosts)
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	event := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 1024) + "\"}}]}\n\n"
	go func() {
		_, _ = io.WriteString(feed, event)
	}()

	resp, err := http.Post(server.URL+passthroughChatPath, "application/json", strings.NewReader(passthroughStreamBody))
	require.NoError(t, err)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "data: "))
	require.NoError(t, resp.Body.Close())

	// Each write blocks until the gateway reads it, and fails once the
	// gateway has closed the upstream body.
	var writeErr error
	require.Eventually(t, func() bool {
		_, writeErr = io.WriteString(feed, event)
		return writeErr != nil
	}, 5*time.Second, 5*time.Millisecond, "the gateway kept relaying to a client that is gone")
	assert.ErrorIs(t, writeErr, io.ErrClosedPipe, "the gateway closed the upstream body")
}
