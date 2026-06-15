package xai

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "xai-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "grok-voice-latest"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://api.x.ai/v1/realtime?") {
		t.Errorf("url = %q, want xAI realtime endpoint", target.URL)
	}
	parsed, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	if got := parsed.Query().Get("model"); got != "grok-voice-latest" {
		t.Errorf("model query = %q, want %q", got, "grok-voice-latest")
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want bearer with key", got)
	}

	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
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

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://custom.x.example/v1")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://custom.x.example/v1/realtime") {
		t.Errorf("url = %q, want the SetBaseURL host", target.URL)
	}
}
