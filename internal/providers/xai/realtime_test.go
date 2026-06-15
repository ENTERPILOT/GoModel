package xai

import (
	"context"
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
	if got := target.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want bearer with key", got)
	}

	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}
