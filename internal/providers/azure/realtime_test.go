package azure

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "azure-secret-key"
	p := New(providers.ProviderConfig{
		APIKey:     apiKey,
		BaseURL:    "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
		APIVersion: "2025-04-01-preview",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "myres.openai.azure.com" || u.Path != "/openai/realtime" {
		t.Errorf("endpoint = %q, want wss://myres.openai.azure.com/openai/realtime", target.URL)
	}
	if got := u.Query().Get("deployment"); got != "gpt-realtime" {
		t.Errorf("deployment = %q, want gpt-realtime", got)
	}
	if got := u.Query().Get("api-version"); got != "2025-04-01-preview" {
		t.Errorf("api-version = %q, want 2025-04-01-preview", got)
	}
	// Azure authenticates with the api-key header, not Bearer.
	if got := target.Headers.Get("api-key"); got != apiKey {
		t.Errorf("api-key = %q, want %q", got, apiKey)
	}
	if target.Headers.Get("Authorization") != "" {
		t.Error("Authorization header must not be set for Azure (uses api-key)")
	}
}

func TestRealtimeTargetStripsExistingOpenAIPath(t *testing.T) {
	// A base already rooted at /openai must not yield /openai/openai/realtime.
	for _, base := range []string{
		"https://myres.openai.azure.com/openai",
		"https://myres.openai.azure.com/openai/v1",
	} {
		p := New(providers.ProviderConfig{APIKey: "k", BaseURL: base}, providers.ProviderOptions{}).(*Provider)
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
		if err != nil {
			t.Fatalf("base %q: unexpected error: %v", base, err)
		}
		u, err := url.Parse(target.URL)
		if err != nil {
			t.Fatalf("base %q: parse target url: %v", base, err)
		}
		if u.Path != "/openai/realtime" {
			t.Errorf("base %q: path = %q, want /openai/realtime", base, u.Path)
		}
	}
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "",
		BaseURL: "https://myres.openai.azure.com",
	}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := target.Headers["Api-Key"]; present {
		t.Error("api-key header should be absent when no key is configured")
	}
}

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k", BaseURL: "https://myres.openai.azure.com"}, providers.ProviderOptions{}).(*Provider)
	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRealtimeCallTarget(t *testing.T) {
	const apiKey = "azure-secret-key"
	// Base URLs pointing at a deployment or the openai sub-path must all
	// resolve to the GA resource-root calls endpoint.
	for _, base := range []string{
		"https://myres.openai.azure.com/openai/deployments/gpt-realtime",
		"https://myres.openai.azure.com/openai",
		"https://myres.openai.azure.com/openai/v1",
		"https://myres.openai.azure.com",
	} {
		p := New(providers.ProviderConfig{APIKey: apiKey, BaseURL: base}, providers.ProviderOptions{}).(*Provider)
		target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
		if err != nil {
			t.Fatalf("base %q: unexpected error: %v", base, err)
		}
		if target.URL != "https://myres.openai.azure.com/openai/v1/realtime/calls" {
			t.Errorf("base %q: url = %q, want the GA calls endpoint", base, target.URL)
		}
		// The GA v1 surface takes no api-version parameter.
		if strings.Contains(target.URL, "api-version") {
			t.Errorf("base %q: url = %q, want no api-version on the GA surface", base, target.URL)
		}
		if got := target.Headers.Get("api-key"); got != apiKey {
			t.Errorf("base %q: api-key = %q, want %q", base, got, apiKey)
		}
		if target.Headers.Get("Authorization") != "" {
			t.Errorf("base %q: Authorization must not be set for Azure (uses api-key)", base)
		}
	}
}

func TestRealtimeClientSecretTarget(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "k",
		BaseURL: "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.URL != "https://myres.openai.azure.com/openai/v1/realtime/client_secrets" {
		t.Errorf("url = %q, want the GA client secrets endpoint", target.URL)
	}
}

func TestRealtimeCallTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k", BaseURL: "https://myres.openai.azure.com"}, providers.ProviderOptions{}).(*Provider)
	if _, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: " "}); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRealtimeTargetAttachesByCallID(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "k",
		BaseURL: "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", CallID: "rtc_3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "myres.openai.azure.com" || u.Path != "/openai/v1/realtime" {
		t.Errorf("endpoint = %q, want wss://myres.openai.azure.com/openai/v1/realtime", target.URL)
	}
	if got := u.Query().Get("call_id"); got != "rtc_3" {
		t.Errorf("call_id = %q, want rtc_3", got)
	}
	// The GA attach surface takes neither api-version nor deployment.
	if u.Query().Has("api-version") || u.Query().Has("deployment") {
		t.Errorf("query = %q, want only call_id on the GA attach surface", u.RawQuery)
	}
}
