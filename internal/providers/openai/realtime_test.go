package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

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

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	// Realtime must dial the configured upstream, not a stale default: SetBaseURL
	// (inherited from CompatibleProvider) updates the client, and RealtimeTarget
	// reads the live base URL, so a custom OpenAI-compatible host is honored and
	// the injected key never goes to the wrong host.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://custom.example.com/v1")

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://custom.example.com/v1/realtime") {
		t.Errorf("url = %q, want the SetBaseURL host", target.URL)
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

func TestRealtimeTargetAttachesByCallID(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", CallID: "rtc_42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(target.URL, "call_id=rtc_42") {
		t.Errorf("url = %q, want call_id attach query", target.URL)
	}
	if strings.Contains(target.URL, "model=") {
		t.Errorf("url = %q, want no model query on sideband attach", target.URL)
	}
}

func TestRealtimeCallTarget(t *testing.T) {
	const apiKey = "sk-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.URL != "https://api.openai.com/v1/realtime/calls" {
		t.Errorf("url = %q, want the realtime calls endpoint", target.URL)
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want bearer with key", got)
	}
	if got := target.Headers.Get("OpenAI-Beta"); got != "" {
		t.Errorf("OpenAI-Beta = %q, want unset (GA endpoint rejects the beta header)", got)
	}
}

func TestRealtimeClientSecretTarget(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.URL != "https://api.openai.com/v1/realtime/client_secrets" {
		t.Errorf("url = %q, want the client secrets endpoint", target.URL)
	}
}

func TestRealtimeCallTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	if _, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRealtimeCallTargetFollowsSetBaseURL(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://custom.example.com/v1")

	target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.URL != "https://custom.example.com/v1/realtime/calls" {
		t.Errorf("url = %q, want the SetBaseURL host", target.URL)
	}
}
