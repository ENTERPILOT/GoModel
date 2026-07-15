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
