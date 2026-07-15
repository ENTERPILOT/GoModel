package bailian

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestRealtimeURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string
		wantErr  bool
	}{
		{name: "default mainland host", baseURL: defaultBaseURL, model: "qwen3-omni-flash-realtime", wantBase: "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"},
		{name: "empty falls back to default", baseURL: "", model: "m", wantBase: "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"},
		{name: "international region host preserved", baseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", model: "m", wantBase: "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime"},
		{name: "missing host", baseURL: "not-a-url", model: "m", wantErr: true},
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
				t.Fatalf("invalid URL: %v", parseErr)
			}
			if base := u.Scheme + "://" + u.Host + u.Path; base != tt.wantBase {
				t.Errorf("base = %q, want %q", base, tt.wantBase)
			}
			if u.Query().Get("model") != tt.model {
				t.Errorf("model = %q, want %q", u.Query().Get("model"), tt.model)
			}
		})
	}
}

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "sk-bailian-secret"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "qwen3-omni-flash-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://dashscope.aliyuncs.com/api-ws/v1/realtime?") {
		t.Errorf("url = %q, want DashScope realtime endpoint", target.URL)
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
	// SetBaseURL switches the DashScope region; the realtime host must follow.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://dashscope-intl.aliyuncs.com/compatible-mode/v1")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(target.URL, "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime") {
		t.Errorf("url = %q, want the SetBaseURL region host", target.URL)
	}
}
