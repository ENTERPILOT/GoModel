package openai

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

func TestRealtimeURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string // scheme://host/path before query
		wantErr  bool
	}{
		{name: "default https to wss", baseURL: "https://api.openai.com/v1", model: "gpt-realtime", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "empty base falls back to default", baseURL: "", model: "gpt-realtime", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "trailing slash normalized", baseURL: "https://api.openai.com/v1/", model: "gpt-realtime", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "http maps to ws", baseURL: "http://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime"},
		{name: "wss preserved", baseURL: "wss://example.com/v1", model: "m", wantBase: "wss://example.com/v1/realtime"},
		{name: "unsupported scheme", baseURL: "ftp://example.com/v1", model: "m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := realtimeURL(tt.baseURL, tt.model)
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
			base := u.Scheme + "://" + u.Host + u.Path
			if base != tt.wantBase {
				t.Errorf("base = %q, want %q", base, tt.wantBase)
			}
			if u.Query().Get("model") != tt.model {
				t.Errorf("model query = %q, want %q", u.Query().Get("model"), tt.model)
			}
		})
	}
}

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "sk-secret-key"
	p, ok := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)
	if !ok {
		t.Fatal("New did not return *Provider")
	}

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://api.openai.com/v1/realtime?") {
		t.Errorf("url = %q, want wss realtime endpoint", target.URL)
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want bearer with key", got)
	}
	// The legacy beta header must NOT be sent: the GA realtime endpoint rejects it.
	if got := target.Headers.Get("OpenAI-Beta"); got != "" {
		t.Errorf("OpenAI-Beta = %q, want unset (GA endpoint rejects the beta header)", got)
	}
}

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "  "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: ""}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := target.Headers["Authorization"]; present {
		t.Error("Authorization header should be absent when no API key is configured")
	}
}
