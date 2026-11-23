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

	// identity names no encoding: the answer is read, priced and cleaned of
	// the upstream's costs like one without the header.
	const upstreamUsage = `{"choices":[],"usage":{` + pricedUsage + `,"cost":9,"cost_details":{"upstream_inference_cost":8}}}`
	identity := []struct {
		name    string
		header  http.Header
		request string
		answer  string
	}{
		{name: "JSON", header: jsonHeader(), request: passthroughChatBody, answer: upstreamUsage},
		{name: "stream", header: sseHeader(), request: passthroughStreamBody, answer: "data: " + upstreamUsage + "\n\ndata: [DONE]\n\n"},
	}
	for _, tt := range identity {
		t.Run("identity is not an encoding/"+tt.name, func(t *testing.T) {
			header := tt.header.Clone()
			header.Set("Content-Encoding", "Identity")
			srv, _ := costGateway(t, costPassthroughProvider(header, strings.NewReader(tt.answer)), billCosts(t, 100, true))

			rec := postJSON(t, srv, passthroughChatPath, tt.request)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			payload := rec.Body.Bytes()
			if isSSEContentType(tt.header) {
				payload = usageEventData(t, rec.Body.String())
			} else {
				assert.Equal(t, "0.00004", rec.Header().Get(responseCostHeader))
			}
			assertOneGatewayCost(t, payload, "0.00004")
			assertNoCostDetails(t, payload)
			assert.NotContains(t, rec.Body.String(), ":9")
			assert.NotContains(t, rec.Body.String(), ":8")
		})
	}
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

// An answer is known to be too large only once the limit was read. From then
// on nothing is held: what was read is sent at once, and each later piece when
// it arrives, not when the upstream is done.
func TestResponseCost_OversizedJSONIsRelayedAsItArrivesPastTheLimit(t *testing.T) {
	body := paddedChatAnswer(t, maxObservedJSONResponseBytes+64)
	head, next, last := body[:maxObservedJSONResponseBytes+1], body[maxObservedJSONResponseBytes+1:maxObservedJSONResponseBytes+32], body[maxObservedJSONResponseBytes+32:]
	upstream, feed := io.Pipe()
	defer closePipe(upstream, feed)
	srv, _ := costGateway(t, costPassthroughProvider(jsonHeader(), upstream), returnCosts)
	fed := make(chan error, 1)
	feedPiece := func(piece string) {
		go func() {
			_, err := feed.Write([]byte(piece))
			fed <- err
		}()
	}
	feedPiece(head)

	resp := postToGateway(t, srv, passthroughChatPath, passthroughChatBody)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Values(responseCostHeader), "too large to price")
	// The upstream is still open: each read returns only if the piece was
	// relayed before the upstream sent more.
	for _, piece := range []string{strings.Replace(head, `,"cost":9`, "", 1), next} {
		require.NoError(t, <-fed)
		got := make([]byte, len(piece))
		_, err := io.ReadFull(resp.Body, got)
		require.NoError(t, err, "a piece is held back")
		require.Equal(t, piece, string(got), "the piece is relayed as it is, without the upstream's cost")
		if piece != next {
			feedPiece(next)
		}
	}
	go func() {
		_, _ = feed.Write([]byte(last))
		_ = feed.Close()
	}()
	tail, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, last, string(tail))
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
// counts beside it, and the last one is the cost the usage log records. A
// slowed-down stream is read, and its usage recorded, ahead of what is
// delivered: each event still carries the cost of its own usage.
func TestResponseCost_UsageOnEveryChunk(t *testing.T) {
	chunk := func(delta, usage string) string {
		return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[` + delta + `],"usage":{` + usage + "}}\n\n"
	}
	stream := chunk(`{"index":0,"delta":{"content":"Hi"},"finish_reason":null}`, `"prompt_tokens":10,"completion_tokens":1,"total_tokens":11`) +
		chunk(`{"index":0,"delta":{"content":"!"},"finish_reason":null}`, `"prompt_tokens":10,"completion_tokens":3,"total_tokens":13`) +
		chunk(`{"index":0,"delta":{},"finish_reason":"stop"}`, pricedUsage) +
		"data: [DONE]\n\n"
	slowedDown := func(cfg *Config) { cfg.ModelResolver = &audioSlowdownResolver{factor: 0.01} }
	deliveries := map[string][]func(*Config){
		"as it arrives":                   {returnCosts},
		"delivered later than it is read": {returnCosts, slowedDown},
	}
	for name, configure := range deliveries {
		t.Run(name, func(t *testing.T) {
			provider := costChatProvider()
			provider.streamData = stream
			srv, logger := costGateway(t, provider, configure...)

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
		})
	}
}

// The usage reader takes a stream's usage from a member spelled "usage" in
// those bytes. One spelled with escapes is not read, so it is not priced: it
// loses the upstream's cost and carries none, never the cost of the usage
// event before it.
func TestResponseCost_EscapedUsageNameCarriesNoCostOfAnEarlierEvent(t *testing.T) {
	stream := "data: {\"choices\":[],\"usage\":{" + pricedUsage + "}}\n\n" +
		`data: {"choices":[],"us\u0061ge":{"prompt_tokens":10,"completion_tokens":5000,"total_tokens":5010,"cost":9}}` + "\n\n" +
		"data: [DONE]\n\n"
	srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), strings.NewReader(stream)), returnCosts)

	rec := postJSON(t, srv, passthroughChatPath, passthroughStreamBody)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	events := strings.Split(rec.Body.String(), "\n\n")
	require.GreaterOrEqual(t, len(events), 2, rec.Body.String())
	assert.Equal(t, responseCostAmount, gjson.Get(strings.TrimPrefix(events[0], "data: "), "usage.cost").Raw)
	assert.Equal(t, `data: {"choices":[],"us\u0061ge":{"prompt_tokens":10,"completion_tokens":5000,"total_tokens":5010}}`, events[1])
}

// failsOnceReader reads lead, fails once with err and then reports a clean
// end, like a reader that does not keep its error.
type failsOnceReader struct {
	lead string
	err  error
}

func (r *failsOnceReader) Read(p []byte) (int, error) {
	if r.lead != "" {
		n := copy(p, r.lead)
		r.lead = r.lead[n:]
		return n, nil
	}
	err := r.err
	r.err = io.EOF
	return 0, err
}

// A JSON answer that fails before it was read (before its first byte, in the
// whitespace in front of it, or inside its object) is not relayed as a
// complete one: the client gets a gateway error, and that error carries none
// of the provider's headers, which belong to the answer it did not get.
func TestResponseCost_JSONAnswerFailingBeforeItWasRead(t *testing.T) {
	upstreamHeader := func() http.Header {
		header := jsonHeader()
		header.Set("Cache-Control", "public, max-age=600")
		header.Set("Location", "https://provider.example/elsewhere")
		header.Set("Access-Control-Allow-Origin", "*")
		header.Set("X-Upstream", "1")
		return header
	}
	tests := []struct {
		name string
		lead string
	}{
		{name: "before the first byte"},
		{name: "after whitespace", lead: " \n\t"},
		{name: "inside the object", lead: `{"choices":[],"usage":{` + pricedUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := &failsOnceReader{lead: tt.lead, err: io.ErrUnexpectedEOF}
			srv, logger := costGateway(t, costPassthroughProvider(upstreamHeader(), body), returnCosts)

			rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

			assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
			assert.Equal(t, "provider_error", gjson.Get(rec.Body.String(), "error.type").String(), rec.Body.String())
			assert.Empty(t, logger.entries)
			assert.Empty(t, rec.Header().Values(responseCostHeader))
			for name := range upstreamHeader() {
				if name != "Content-Type" {
					assert.Empty(t, rec.Header().Values(name), "the provider's %s is on a gateway error", name)
				}
			}
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		})
	}

	// An answer that was read is relayed with the provider's headers.
	for _, body := range []string{`{"choices":[],"usage":{` + pricedUsage + `}}`, `[{"usage":{}}]`} {
		srv, _ := costGateway(t, costPassthroughProvider(upstreamHeader(), strings.NewReader(body)), returnCosts)

		rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for name, values := range upstreamHeader() {
			assert.Equal(t, values, rec.Header().Values(name), "%s: %s", name, body)
		}
	}
}

// An event carries the cost of its own usage or none. One whose usage the
// usage reader did not read (a framing it does not know, a usage it skips)
// never carries the cost of the event before it, and once an event ended
// where the reader ends none, it no longer reads the events relayed.
func TestResponseCost_UnreadUsageEventCarriesNoCost(t *testing.T) {
	usageEvent := func(usage string) string { return `data: {"choices":[],"usage":{` + usage + `}}` }
	const done = "data: [DONE]\n\n"
	var (
		first      = usageEvent(`"prompt_tokens":10,"completion_tokens":1,"total_tokens":11`)
		firstCost  = usageEvent(`"prompt_tokens":10,"completion_tokens":1,"total_tokens":11` + costMember("0.000012"))
		second     = usageEvent(`"prompt_tokens":20,"completion_tokens":1,"total_tokens":21`)
		secondCost = usageEvent(`"prompt_tokens":20,"completion_tokens":1,"total_tokens":21` + costMember("0.000022"))
		noTokens   = usageEvent(`"prompt_tokens":0,"completion_tokens":0,"total_tokens":0`)
	)
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "every event read",
			stream: first + "\n\n" + second + "\r\n\r\n" + done,
			want:   firstCost + "\n\n" + secondCost + "\r\n\r\n" + done,
		},
		{
			name:   "bare CR framing after a priced event",
			stream: first + "\n\n" + second + "\r\r" + done,
			want:   firstCost + "\n\n" + second + "\r\r" + done,
		},
		{
			name:   "LF framing again after a bare CR event",
			stream: first + "\n\n" + ": ping\r\r" + second + "\n\n" + done,
			want:   firstCost + "\n\n" + ": ping\r\r" + second + "\n\n" + done,
		},
		{
			name:   "blank line of an LF then a CR",
			stream: first + "\n\r" + second + "\n\n" + done,
			want:   first + "\n\r" + second + "\n\n" + done,
		},
		{
			name:   "blank line of a CRLF then a CR",
			stream: first + "\r\n\r" + second + "\n\n" + done,
			want:   first + "\r\n\r" + second + "\n\n" + done,
		},
		{
			name:   "usage without tokens after a priced event",
			stream: first + "\n\n" + noTokens + "\n\n" + done,
			want:   firstCost + "\n\n" + noTokens + "\n\n" + done,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), strings.NewReader(tt.stream)), returnCosts)

			rec := postJSON(t, srv, passthroughChatPath, passthroughStreamBody)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.want, rec.Body.String())
		})
	}
}

// A client that goes away mid-stream ends the relay: the gateway stops
// writing and closes the upstream body instead of draining it.
func TestResponseCost_ClientDisconnectMidStream(t *testing.T) {
	upstream, feed := io.Pipe()
	defer closePipe(upstream, feed)
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
