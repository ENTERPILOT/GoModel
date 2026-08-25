package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// realtimeMockProvider is a mockProvider that also implements core.RealtimeProvider
// and core.RealtimeCallProvider.
type realtimeMockProvider struct {
	mockProvider
	lastReq     *core.RealtimeRequest
	lastCallReq *core.RealtimeRequest
}

func (m *realtimeMockProvider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	m.lastReq = req
	return &core.RealtimeTarget{
		URL:     "wss://upstream.example/v1/realtime?model=" + req.Model,
		Headers: http.Header{"Authorization": {"Bearer test"}},
	}, nil
}

func (m *realtimeMockProvider) RealtimeCallTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	m.lastCallReq = req
	return &core.RealtimeHTTPTarget{URL: "https://upstream.example/v1/realtime/calls"}, nil
}

func (m *realtimeMockProvider) RealtimeClientSecretTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	m.lastCallReq = req
	return &core.RealtimeHTTPTarget{URL: "https://upstream.example/v1/realtime/client_secrets"}, nil
}

// intentRealtimeMockProvider also implements core.RealtimeIntentProvider, like
// the providers that serve specialized session surfaces. The bare
// realtimeMockProvider stands in for a conversation-only realtime provider.
type intentRealtimeMockProvider struct {
	realtimeMockProvider
	intents []string
}

func (m *intentRealtimeMockProvider) SupportsRealtimeIntent(intent string) bool {
	for _, supported := range m.intents {
		if core.EqualRealtimeIntent(intent, supported) {
			return true
		}
	}
	return false
}

func TestRouterRealtimeTargetRoutesByModel(t *testing.T) {
	rt := &realtimeMockProvider{}
	lookup := newMockLookup()
	lookup.addModel("gpt-realtime", rt, "openai")
	router, _ := NewRouter(lookup)

	target, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(target.URL, "model=gpt-realtime") {
		t.Errorf("url = %q, want model in query", target.URL)
	}
	if rt.lastReq == nil || rt.lastReq.Model != "gpt-realtime" {
		t.Errorf("provider received %+v, want forwarded model", rt.lastReq)
	}
}

func TestRouterRealtimeTargetUnsupportedModel(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("plain", &mockProvider{}, "openai") // no RealtimeProvider
	router, _ := NewRouter(lookup)

	_, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "plain"})
	if err == nil || !strings.Contains(err.Error(), "does not support realtime") {
		t.Fatalf("err = %v, want does-not-support-realtime", err)
	}
}

func TestRouterRealtimeTargetForwardsCallID(t *testing.T) {
	rt := &realtimeMockProvider{}
	lookup := newMockLookup()
	lookup.addModel("gpt-realtime", rt, "openai")
	router, _ := NewRouter(lookup)

	_, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", CallID: "rtc_7"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.lastReq == nil || rt.lastReq.CallID != "rtc_7" {
		t.Errorf("provider received %+v, want forwarded call id", rt.lastReq)
	}
}

func TestRouterRealtimeTargetForwardsIntent(t *testing.T) {
	// Transcription sessions carry intent=transcription end to end; the router
	// must not drop it while re-shaping the request around the resolved selector.
	rt := &intentRealtimeMockProvider{intents: []string{core.RealtimeIntentTranscription}}
	lookup := newMockLookup()
	lookup.addModel("gpt-4o-transcribe", rt, "openai")
	router, _ := NewRouter(lookup)

	_, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-4o-transcribe", Intent: "transcription"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.lastReq == nil || rt.lastReq.Intent != "transcription" {
		t.Errorf("provider received %+v, want forwarded intent", rt.lastReq)
	}
}

func TestRouterRealtimeCallTargetsForwardIntent(t *testing.T) {
	// The signaling routes carry the intent too: it is what points the provider
	// at its translation surface instead of the conversation one.
	rt := &intentRealtimeMockProvider{intents: []string{core.RealtimeIntentTranslation}}
	lookup := newMockLookup()
	lookup.addModel("gpt-realtime-translate", rt, "openai")
	router, _ := NewRouter(lookup)

	req := &core.RealtimeRequest{Model: "gpt-realtime-translate", Intent: core.RealtimeIntentTranslation}
	if _, err := router.RealtimeCallTarget(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.lastCallReq == nil || rt.lastCallReq.Intent != core.RealtimeIntentTranslation {
		t.Errorf("call provider received %+v, want forwarded intent", rt.lastCallReq)
	}

	rt.lastCallReq = nil
	if _, err := router.RealtimeClientSecretTarget(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.lastCallReq == nil || rt.lastCallReq.Intent != core.RealtimeIntentTranslation {
		t.Errorf("client secret provider received %+v, want forwarded intent", rt.lastCallReq)
	}
}

func TestRouterRealtimeCallTargetRoutesByModel(t *testing.T) {
	rt := &realtimeMockProvider{}
	lookup := newMockLookup()
	lookup.addModel("gpt-realtime", rt, "openai")
	router, _ := NewRouter(lookup)

	target, err := router.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(target.URL, "/realtime/calls") {
		t.Errorf("url = %q, want the calls endpoint", target.URL)
	}
	if rt.lastCallReq == nil || rt.lastCallReq.Model != "gpt-realtime" {
		t.Errorf("provider received %+v, want forwarded model", rt.lastCallReq)
	}
}

func TestRouterRealtimeClientSecretTargetRoutesByModel(t *testing.T) {
	rt := &realtimeMockProvider{}
	lookup := newMockLookup()
	lookup.addModel("gpt-realtime", rt, "openai")
	router, _ := NewRouter(lookup)

	target, err := router.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(target.URL, "/realtime/client_secrets") {
		t.Errorf("url = %q, want the client secrets endpoint", target.URL)
	}
}

func TestRouterRealtimeCallTargetUnsupportedModel(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("plain", &mockProvider{}, "openai") // no RealtimeCallProvider
	router, _ := NewRouter(lookup)

	_, err := router.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "plain"})
	if err == nil || !strings.Contains(err.Error(), "does not support realtime calls") {
		t.Fatalf("err = %v, want does-not-support-realtime-calls", err)
	}
}

func TestRouterRealtimeTargetWithProviderHint(t *testing.T) {
	// The passthrough route reuses RealtimeTarget by passing the path provider as
	// the resolution hint; a registry-backed lookup exercises that mapping.
	rt := &realtimeMockProvider{}
	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     rt,
		providerName: "openai",
		providerType: "openai",
		modelID:      "gpt-realtime",
	})
	registry.initialized = true // same-package test shortcut: skip network init
	router, _ := NewRouter(registry)

	target, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", Provider: "openai"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target == nil || target.URL == "" {
		t.Fatal("expected a realtime target")
	}
}

func TestRouterRealtimeIntentRejectedByProviderWithoutSupport(t *testing.T) {
	// A provider that does not serve a specialized surface must not receive the
	// request at all: silently building a conversation target would answer a
	// translation session — or mint a client secret for one — with the wrong
	// capability. Every realtime surface is gated the same way.
	conversationOnly := &realtimeMockProvider{}
	translationOnly := &intentRealtimeMockProvider{intents: []string{core.RealtimeIntentTranslation}}
	lookup := newMockLookup()
	lookup.addModel("conversation-only", conversationOnly, "xai")
	lookup.addModel("translator", translationOnly, "openai")
	router, _ := NewRouter(lookup)

	surfaces := map[string]func(*core.RealtimeRequest) error{
		"websocket": func(req *core.RealtimeRequest) error {
			_, err := router.RealtimeTarget(context.Background(), req)
			return err
		},
		"calls": func(req *core.RealtimeRequest) error {
			_, err := router.RealtimeCallTarget(context.Background(), req)
			return err
		},
		"client secrets": func(req *core.RealtimeRequest) error {
			_, err := router.RealtimeClientSecretTarget(context.Background(), req)
			return err
		},
	}
	cases := map[string]struct {
		model  string
		intent string
	}{
		"unsupported provider": {model: "conversation-only", intent: core.RealtimeIntentTranslation},
		"unsupported intent":   {model: "translator", intent: core.RealtimeIntentTranscription},
		"unknown intent":       {model: "translator", intent: "dictation"},
	}
	for surface, call := range surfaces {
		for name, tc := range cases {
			t.Run(surface+"/"+name, func(t *testing.T) {
				err := call(&core.RealtimeRequest{Model: tc.model, Intent: tc.intent})
				if err == nil || !strings.Contains(err.Error(), "does not support "+tc.intent+" realtime sessions") {
					t.Fatalf("err = %v, want %s rejected for %q", err, tc.intent, tc.model)
				}
			})
		}
	}

	// Conversation sessions carry no intent and stay unaffected.
	if _, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "conversation-only"}); err != nil {
		t.Fatalf("conversation session rejected: %v", err)
	}
}

func TestRouterRealtimeIntentAcceptsPaddedCasing(t *testing.T) {
	// Intents arrive from a query parameter, so the gate compares them the way
	// providers do (Postel): trimmed and case-insensitively.
	rt := &intentRealtimeMockProvider{intents: []string{core.RealtimeIntentTranslation}}
	lookup := newMockLookup()
	lookup.addModel("translator", rt, "openai")
	router, _ := NewRouter(lookup)

	if _, err := router.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "translator", Intent: " Translation "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
