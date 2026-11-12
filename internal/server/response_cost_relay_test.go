package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// postToGateway sends body to path of a gateway served over a real
// connection. The request is abandoned after half a minute, so a response
// that is held back fails the test instead of hanging it.
func postToGateway(t *testing.T, srv *Server, path, body string) *http.Response {
	t.Helper()
	gateway := httptest.NewServer(srv)
	t.Cleanup(gateway.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := gateway.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// An upstream may send a JSON body that is not one object over time: Gemini's
// streamGenerateContent without alt=sse sends an array, one element per
// chunk. Each piece reaches the client when it arrives, not when the upstream
// is done, and the body is relayed as it is.
func TestResponseCost_StreamedJSONArrayIsRelayedAsItArrives(t *testing.T) {
	const (
		first = "[{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n,"
		rest  = "\n{\"candidates\":[],\"usageMetadata\":{\"totalTokenCount\":3},\"usage\":{\"cost\":9}}]"
	)
	for _, mode := range []struct {
		name      string
		configure func(*testing.T) func(*Config)
	}{
		{name: "markups off", configure: func(*testing.T) func(*Config) { return returnCosts }},
		{name: "markups on", configure: func(t *testing.T) func(*Config) { return billCosts(t, 50, true) }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			upstream, feed := io.Pipe()
			srv, _ := costGateway(t, costPassthroughProvider(jsonHeader(), upstream), mode.configure(t))
			fed := make(chan error, 1)
			go func() {
				_, err := feed.Write([]byte(first))
				fed <- err
			}()

			resp := postToGateway(t, srv, passthroughChatPath, passthroughChatBody)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, <-fed)
			// The upstream is still open: this read returns only if the
			// first piece was relayed before the upstream finished.
			got := make([]byte, len(first))
			_, err := io.ReadFull(resp.Body, got)
			require.NoError(t, err, "the first piece is held back")
			assert.Equal(t, first, string(got))

			go func() {
				_, _ = feed.Write([]byte(rest))
				_ = feed.Close()
			}()
			tail, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, rest, string(tail), "an array is relayed as it is, its members included")
			assert.Empty(t, resp.Header.Values(responseCostHeader), "priced after it was sent")
		})
	}
}

// A body that grows as it is rewritten is framed for the length it has: an
// upstream's Content-Length is not relayed, and the client reads all of it.
func TestResponseCost_RewrittenBodyIsFramedForItsLength(t *testing.T) {
	answer := func(padding int) string {
		return `{"id":"x","text":"` + strings.Repeat("a", padding) + `","choices":[],"usage":{` + pricedUsage + `}}`
	}
	tests := []struct {
		name    string
		padding int
	}{
		{name: "small enough for the server to count", padding: 16},
		{name: "chunked", padding: 64 << 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := answer(tt.padding)
			header := jsonHeader()
			header.Set("Content-Length", strconv.Itoa(len(upstream)))
			srv, _ := costGateway(t, costPassthroughProvider(header, strings.NewReader(upstream)), returnCosts)

			resp := postToGateway(t, srv, passthroughChatPath, passthroughChatBody)
			body, err := io.ReadAll(resp.Body)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			want := strings.Replace(upstream, pricedUsage, pricedUsage+costMember(responseCostAmount), 1)
			assert.Equal(t, want, string(body))
			assert.True(t, gjson.ValidBytes(body))
			if resp.ContentLength != -1 {
				assert.Equal(t, int64(len(body)), resp.ContentLength)
			} else {
				assert.Equal(t, []string{"chunked"}, resp.TransferEncoding)
			}
			assert.Equal(t, []string{responseCostAmount}, resp.Header.Values(responseCostHeader))
		})
	}
}

// A stream whose lines end in bare CRs, or in a mix of endings, reaches the
// client event by event over a real connection, with the upstream's cost
// replaced.
func TestResponseCost_StreamsInEveryFramingArriveEventByEvent(t *testing.T) {
	const usageEvent = `data: {"choices":[],"usage":{` + pricedUsage + `,"cost":9}}`
	tests := []struct {
		name, blank string
		wantCost    string // empty: the usage reader does not read this framing
	}{
		{name: "LF", blank: "\n\n", wantCost: responseCostAmount},
		{name: "CRLF", blank: "\r\n\r\n", wantCost: responseCostAmount},
		{name: "bare CR", blank: "\r\r"},
		{name: "LF then CRLF", blank: "\n\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, feed := io.Pipe()
			srv, _ := costGateway(t, costPassthroughProvider(sseHeader(), upstream), returnCosts)
			fed := make(chan error, 1)
			go func() {
				_, err := feed.Write([]byte(usageEvent + tt.blank))
				fed <- err
			}()

			resp := postToGateway(t, srv, passthroughChatPath, passthroughStreamBody)

			require.NoError(t, <-fed)
			want := `data: {"choices":[],"usage":{` + pricedUsage + `}}` + tt.blank
			if tt.wantCost != "" {
				want = `data: {"choices":[],"usage":{` + pricedUsage + costMember(tt.wantCost) + `}}` + tt.blank
			}
			got := make([]byte, len(want))
			_, err := io.ReadFull(resp.Body, got)
			require.NoError(t, err, "the event is held back")
			assert.Equal(t, want, string(got))

			go func() {
				_, _ = feed.Write([]byte("data: [DONE]" + tt.blank))
				_ = feed.Close()
			}()
			tail, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, "data: [DONE]"+tt.blank, string(tail))
		})
	}
}
