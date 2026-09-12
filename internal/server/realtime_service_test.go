package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsWebSocketUpgrade(t *testing.T) {
	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{name: "standard handshake", connection: "Upgrade", upgrade: "websocket", want: true},
		{name: "case-insensitive", connection: "keep-alive, upgrade", upgrade: "WebSocket", want: true},
		{name: "missing upgrade header", connection: "Upgrade", upgrade: "", want: false},
		{name: "wrong upgrade target", connection: "Upgrade", upgrade: "h2c", want: false},
		{name: "no connection upgrade token", connection: "keep-alive", upgrade: "websocket", want: false},
		{name: "plain request", connection: "", upgrade: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet, "/v1/realtime", nil)
			if tt.connection != "" {
				r.Header.Set("Connection", tt.connection)
			}
			if tt.upgrade != "" {
				r.Header.Set("Upgrade", tt.upgrade)
			}
			assert.Equal(t, tt.want, isWebSocketUpgrade(r))
		})
	}
}

func TestRealtimeUpstreamHeaders(t *testing.T) {
	client := http.Header{}
	client.Set("Authorization", "Bearer client-secret") // must never reach upstream
	client.Set("Sec-WebSocket-Key", "abc123")           // handshake header, dialer regenerates
	client.Set("Sec-WebSocket-Version", "13")
	client.Set("OpenAI-Beta", "realtime=v1") // legacy header the GA endpoint rejects
	client.Set("X-Custom", "keep-me")

	target := http.Header{}
	target.Set("Authorization", "Bearer upstream-key")

	got := realtimeUpstreamHeaders(context.Background(), client, target)

	assert.Equal(t, "Bearer upstream-key", got.Get("Authorization"))
	assert.Empty(t, got.Get("OpenAI-Beta"))
	assert.Equal(t, "keep-me", got.Get("X-Custom"))

	for key := range got {
		assert.False(t, strings.HasPrefix(http.CanonicalHeaderKey(key), "Sec-Websocket"), "handshake header leaked upstream: %q", key)
	}
}

func TestRealtimeForwardsTrimmedIntent(t *testing.T) {
	// The handler translates the intent query parameter into the provider
	// request: trimmed, alongside the resolved model. The websocket dial fails
	// (no upstream), but the request is captured before the dial.
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
	}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Get(t, "/v1/realtime?model=gpt-4o-transcribe&intent=%20transcription%20")

	_ = handler.Realtime(c)

	require.NotNil(t, mock.capturedRealtime, "router received no request (status %d, body %s)", rec.Code, rec.Body.String())
	assert.Equal(t, "transcription", mock.capturedRealtime.Intent)
	assert.Equal(t, "gpt-4o-transcribe", mock.capturedRealtime.Model)
	assert.Empty(t, mock.capturedRealtime.CallID)
}
