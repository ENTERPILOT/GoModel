package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"gomodel/internal/core"
)

// realtimeMockProvider is a mockProvider that also implements core.RealtimeProvider.
type realtimeMockProvider struct {
	mockProvider
	lastReq *core.RealtimeRequest
}

func (m *realtimeMockProvider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	m.lastReq = req
	return &core.RealtimeTarget{
		URL:     "wss://upstream.example/v1/realtime?model=" + req.Model,
		Headers: http.Header{"Authorization": {"Bearer test"}},
	}, nil
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
