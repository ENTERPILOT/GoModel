package llmclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func streamServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestClient_DoStream_StreamStartFailures(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "empty SSE body",
			contentType: "text/event-stream",
			wantStatus:  http.StatusBadGateway,
			wantMessage: "empty stream",
		},
		{
			name:        "empty body without content type",
			wantStatus:  http.StatusBadGateway,
			wantMessage: "empty stream",
		},
		{
			name:        "only keep-alive comments",
			contentType: "text/event-stream",
			body:        ": ping\n\n: ping\n\n",
			wantStatus:  http.StatusBadGateway,
			wantMessage: "empty stream",
		},
		{
			name:        "keep-alive events beyond the hold-back limit",
			contentType: "text/event-stream",
			body:        strings.Repeat(": keep-alive\n\n", maxStreamStartBytes/14+10),
			wantStatus:  http.StatusBadGateway,
			wantMessage: "empty stream",
		},
		{
			name:        "comment lines beyond the hold-back limit without blank lines",
			contentType: "text/event-stream",
			body:        strings.Repeat(": pad\n", maxStreamStartBytes/6+10),
			wantStatus:  http.StatusBadGateway,
			wantMessage: "empty stream",
		},
		{
			name:        "in-band 429 as first event",
			contentType: "text/event-stream",
			body:        "data: {\"error\":{\"message\":\"Rate limit exceeded\",\"type\":\"rate_limit_error\",\"code\":429}}\n\n",
			wantStatus:  http.StatusTooManyRequests,
			wantMessage: "Rate limit exceeded",
		},
		{
			name:        "in-band error after a comment and event line",
			contentType: "text/event-stream; charset=utf-8",
			body:        ": processing\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
			wantStatus:  http.StatusBadGateway,
			wantMessage: "Overloaded",
		},
		{
			name:        "in-band error at EOF without trailing blank line",
			contentType: "text/event-stream",
			body:        "data: {\"error\":{\"message\":\"upstream down\",\"code\":503}}",
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "upstream down",
		},
		{
			name:        "in-band error after keep-alives beyond the hold-back limit",
			contentType: "text/event-stream",
			body:        strings.Repeat(": keep-alive\n\n", maxStreamStartBytes/14+10) + "data: {\"error\":{\"message\":\"slow down\",\"code\":429}}\n\n",
			wantStatus:  http.StatusTooManyRequests,
			wantMessage: "slow down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := streamServer(t, tt.contentType, tt.body)
			client := New(DefaultConfig("test", server.URL), nil)

			stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
			require.Nil(t, stream)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantStatus, gatewayErr.StatusCode)
			assert.Contains(t, gatewayErr.Message, tt.wantMessage)
		})
	}
}

func TestClient_DoStream_StreamStartReplaysHealthyStreams(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "chat chunks",
			contentType: "text/event-stream",
			body:        "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:        "comment before first chunk",
			contentType: "text/event-stream",
			body:        ": OPENROUTER PROCESSING\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:        "CRLF framed named events",
			contentType: "text/event-stream",
			body:        "event: message_start\r\ndata: {\"type\":\"message_start\"}\r\n\r\nevent: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
		},
		{
			name:        "chunk that mentions error in content",
			contentType: "text/event-stream",
			body:        "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"error\\\":1}\"}}]}\n\n",
		},
		{
			name:        "first line longer than the peek buffer",
			contentType: "text/event-stream",
			body:        "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("a", 4*streamPeekBytes) + "\"}}]}\n\n",
		},
		{
			name:        "first line longer than the hold-back limit",
			contentType: "text/event-stream",
			body:        "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("a", maxStreamStartBytes+1024) + "\"}}]}\n\n",
		},
		{
			name:        "non-SSE stream",
			contentType: "application/vnd.amazon.eventstream",
			body:        "\x00\x00\x00binary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := streamServer(t, tt.contentType, tt.body)
			client := New(DefaultConfig("test", server.URL), nil)

			stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
			require.NoError(t, err)
			defer stream.Close()

			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(got))
		})
	}
}

func TestClient_DoStream_StreamStartDropsOversizedKeepAlives(t *testing.T) {
	// Keep-alives beyond the hold-back limit carry no data, so they are
	// dropped rather than buffered; everything from the first event on is kept.
	events := "event: message_start\ndata: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n"
	body := strings.Repeat(": keep-alive\n\n", maxStreamStartBytes/14+10) + events
	server := streamServer(t, "text/event-stream", body)
	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)
	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(got), events), "the first event must replay intact")
	assert.Less(t, len(got), len(body))
}

func TestClient_DoStream_EmptyStreamRecordsFailure(t *testing.T) {
	server := streamServer(t, "text/event-stream", "")

	var lastInfo ResponseInfo
	config := DefaultConfig("test", server.URL)
	config.Hooks.OnRequestEnd = func(_ context.Context, info ResponseInfo) {
		lastInfo = info
	}
	client := New(config, nil)

	_, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, lastInfo.StatusCode)
	assert.Error(t, lastInfo.Error)
}
