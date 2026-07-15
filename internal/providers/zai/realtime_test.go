package zai

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "zai-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://api.z.ai/api/paas/v4/realtime?") {
		t.Errorf("url = %q, want Z.ai realtime endpoint", target.URL)
	}
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	if got := u.Query().Get("model"); got != "glm-realtime" {
		t.Errorf("model query = %q, want %q", got, "glm-realtime")
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want bearer with key", got)
	}
}

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	// open.bigmodel.cn region must be honored when configured via ZAI_BASE_URL.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://open.bigmodel.cn/api/paas/v4")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://open.bigmodel.cn/api/paas/v4/realtime?") {
		t.Errorf("url = %q, want the configured region host", target.URL)
	}
}

func TestRealtimeTargetNormalizesCodingPlanBase(t *testing.T) {
	// The GLM Coding Plan base (/api/coding/paas/v4) must still resolve to the
	// fixed realtime path /api/paas/v4/realtime, not /api/coding/paas/v4/realtime.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://api.z.ai/api/coding/paas/v4")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	if u.Path != "/api/paas/v4/realtime" {
		t.Errorf("path = %q, want /api/paas/v4/realtime", u.Path)
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

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}
