package providers

import (
	"net/url"
	"testing"
)

func TestOpenAIRealtimeURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string // scheme://host/path before query
		wantErr  bool
	}{
		{name: "openai https to wss", baseURL: "https://api.openai.com/v1", model: "gpt-realtime", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "xai https to wss", baseURL: "https://api.x.ai/v1", model: "grok-voice-latest", wantBase: "wss://api.x.ai/v1/realtime"},
		{name: "trailing slash normalized", baseURL: "https://api.openai.com/v1/", model: "m", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "http maps to ws", baseURL: "http://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime"},
		{name: "ws preserved", baseURL: "ws://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime"},
		{name: "wss preserved", baseURL: "wss://example.com/v1", model: "m", wantBase: "wss://example.com/v1/realtime"},
		{name: "empty base", baseURL: "", model: "m", wantErr: true},
		{name: "malformed url", baseURL: "http://[::1", model: "m", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://example.com/v1", model: "m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenAIRealtimeURL(tt.baseURL, tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			u, parseErr := url.Parse(got)
			if parseErr != nil {
				t.Fatalf("result is not a valid URL: %v", parseErr)
			}
			if base := u.Scheme + "://" + u.Host + u.Path; base != tt.wantBase {
				t.Errorf("base = %q, want %q", base, tt.wantBase)
			}
			if u.Query().Get("model") != tt.model {
				t.Errorf("model query = %q, want %q", u.Query().Get("model"), tt.model)
			}
		})
	}
}

func TestOpenAIRealtimeURLTrimsModel(t *testing.T) {
	got, err := OpenAIRealtimeURL("https://api.openai.com/v1", "  gpt-realtime  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u, _ := url.Parse(got)
	if m := u.Query().Get("model"); m != "gpt-realtime" {
		t.Errorf("model query = %q, want trimmed %q", m, "gpt-realtime")
	}
}
