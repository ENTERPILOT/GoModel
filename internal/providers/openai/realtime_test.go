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

func TestRealtimeTargetTranslationIntent(t *testing.T) {
	// A translation session dials the dedicated translations endpoint and, unlike
	// a transcription session, keeps the model in the URL. It reports no usage
	// events of its own, so the provider asks the gateway to meter the audio it
	// relays.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	for _, intent := range []string{"translation", " Translation "} {
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime-translate", Intent: intent})
		if err != nil {
			t.Fatalf("intent %q: unexpected error: %v", intent, err)
		}
		if target.URL != "wss://api.openai.com/v1/realtime/translations?model=gpt-realtime-translate" {
			t.Errorf("intent %q: url = %q, want the translations endpoint with the model", intent, target.URL)
		}
		if !target.MeterInputAudio {
			t.Errorf("intent %q: MeterInputAudio = false, want the session metered", intent)
		}
		// The model is in the URL, so nothing has to be pinned in-session.
		if target.PinSessionModel != "" {
			t.Errorf("intent %q: PinSessionModel = %q, want empty", intent, target.PinSessionModel)
		}
		if got := target.Headers.Get("Authorization"); got != "Bearer k" {
			t.Errorf("intent %q: Authorization = %q, want bearer with key", intent, got)
		}
	}
}

func TestRealtimeTargetConversationIsNotMetered(t *testing.T) {
	// Conversation sessions report usage in response.done events, so the gateway
	// must not meter (and double-count) their audio.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.MeterInputAudio {
		t.Error("MeterInputAudio = true, want false for a session that reports its own usage")
	}
}

func TestRealtimeHTTPTargetsTranslationIntent(t *testing.T) {
	// Translation sessions sign WebRTC calls and mint client secrets on the same
	// dedicated surface as their websocket.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	req := &core.RealtimeRequest{Model: "gpt-realtime-translate", Intent: core.RealtimeIntentTranslation}

	call, err := p.RealtimeCallTarget(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.URL != "https://api.openai.com/v1/realtime/translations/calls" {
		t.Errorf("calls url = %q, want the translations calls endpoint", call.URL)
	}

	secret, err := p.RealtimeClientSecretTarget(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secret.URL != "https://api.openai.com/v1/realtime/translations/client_secrets" {
		t.Errorf("client secrets url = %q, want the translations client secrets endpoint", secret.URL)
	}
}

func TestRealtimeTargetTranscriptionIntent(t *testing.T) {
	// A transcription session must dial ?intent=transcription without a model
	// parameter: OpenAI rejects transcription models as the session model, and
	// picks the actual model later via session.update. The requested model only
	// routes the request inside the gateway.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	for _, intent := range []string{"transcription", " Transcription "} {
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-4o-transcribe", Intent: intent})
		if err != nil {
			t.Fatalf("intent %q: unexpected error: %v", intent, err)
		}
		if target.URL != "wss://api.openai.com/v1/realtime?intent=transcription" {
			t.Errorf("intent %q: url = %q, want intent-only realtime URL", intent, target.URL)
		}
		// The URL carries no model, so the provider must ask the gateway to pin
		// the session.update model selection to the routed model.
		if target.PinSessionModel != "gpt-4o-transcribe" {
			t.Errorf("intent %q: PinSessionModel = %q, want the routed model", intent, target.PinSessionModel)
		}
		// Transcription models usually report usage in their completed event,
		// but a model that omits it would otherwise leave the session free, so
		// the relayed audio has to back it. The gateway meters it only when the
		// session reports nothing, so this never double-bills the common case.
		if !target.MeterInputAudio {
			t.Errorf("intent %q: MeterInputAudio = false, want the session metered as a fallback", intent)
		}
	}

	// The model still gates the request: without one there is nothing to route
	// or attribute usage to, transcription intent or not.
	if _, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Intent: "transcription"}); err == nil {
		t.Error("expected error for transcription intent without model")
	}

	// Unknown intents keep today's conversation-session behavior.
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", Intent: "conversation"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(target.URL, "model=gpt-realtime") {
		t.Errorf("url = %q, want model parameter for non-transcription intent", target.URL)
	}
	if target.PinSessionModel != "" {
		t.Errorf("PinSessionModel = %q, want empty: the URL already fixes the model", target.PinSessionModel)
	}
}

func TestSupportsRealtimeIntent(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	cases := map[string]bool{
		core.RealtimeIntentTranscription: true,
		core.RealtimeIntentTranslation:   true,
		" Translation ":                  true, // query parameters arrive padded
		"conversation":                   false,
		"dictation":                      false,
		"":                               false,
	}
	for intent, want := range cases {
		if got := p.SupportsRealtimeIntent(intent); got != want {
			t.Errorf("SupportsRealtimeIntent(%q) = %v, want %v", intent, got, want)
		}
	}
}
