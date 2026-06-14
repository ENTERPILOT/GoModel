package server

import (
	"context"
	"net/http"
	"testing"
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
			if got := isWebSocketUpgrade(r); got != tt.want {
				t.Errorf("isWebSocketUpgrade = %v, want %v", got, tt.want)
			}
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

	if got.Get("Authorization") != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want injected upstream key", got.Get("Authorization"))
	}
	if got.Get("OpenAI-Beta") != "" {
		t.Errorf("OpenAI-Beta = %q, want stripped (GA endpoint rejects it)", got.Get("OpenAI-Beta"))
	}
	if got.Get("X-Custom") != "keep-me" {
		t.Errorf("X-Custom = %q, want forwarded", got.Get("X-Custom"))
	}
	for key := range got {
		if len(key) >= 13 && http.CanonicalHeaderKey(key)[:13] == "Sec-Websocket" {
			t.Errorf("handshake header leaked upstream: %q", key)
		}
	}
}
