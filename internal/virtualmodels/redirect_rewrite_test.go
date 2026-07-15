package virtualmodels

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type chatExecMock struct {
	supported map[string]bool
	captured  *core.ChatRequest
	resp      *core.ChatResponse
}

func (m *chatExecMock) Supports(model string) bool { return m.supported[model] }

func (m *chatExecMock) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	m.captured = req
	return m.resp, nil
}

func TestChatExecutor_RewritesRedirectModel(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	inner := &chatExecMock{
		supported: map[string]bool{"openai/gpt-4o": true},
		resp:      &core.ChatResponse{ID: "chatcmpl_1", Model: "gpt-4o"},
	}
	executor := NewChatExecutor(inner, svc)

	resp, err := executor.ChatCompletion(ctx, &core.ChatRequest{Model: "fast"})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp == nil || resp.ID != "chatcmpl_1" {
		t.Fatalf("response = %+v, want passthrough of inner response", resp)
	}
	if inner.captured == nil {
		t.Fatal("inner provider was not called")
	}
	if inner.captured.Model != "gpt-4o" || inner.captured.Provider != "openai" {
		t.Fatalf("forwarded selector = %s/%s, want openai/gpt-4o", inner.captured.Provider, inner.captured.Model)
	}
}

func TestChatExecutor_PassesThroughConcreteModel(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	inner := &chatExecMock{
		supported: map[string]bool{"openai/gpt-4o": true},
		resp:      &core.ChatResponse{ID: "chatcmpl_2"},
	}
	executor := NewChatExecutor(inner, svc)

	if _, err := executor.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o", Provider: "openai"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.captured == nil || inner.captured.Model != "gpt-4o" || inner.captured.Provider != "openai" {
		t.Fatalf("forwarded request = %+v, want unchanged selector", inner.captured)
	}
}

func TestChatExecutor_UnknownModelReturnsNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	inner := &chatExecMock{supported: map[string]bool{}}
	executor := NewChatExecutor(inner, svc)

	_, err := executor.ChatCompletion(context.Background(), &core.ChatRequest{Model: "missing"})
	if err == nil {
		t.Fatal("ChatCompletion() error = nil, want model_not_found")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code == nil || *gatewayErr.Code != "model_not_found" {
		t.Fatalf("error = %v, want model_not_found gateway error", err)
	}
	if inner.captured != nil {
		t.Fatal("inner provider must not be called for unsupported models")
	}
}

// Translated request rewriting keeps the resolved provider because downstream
// routing still needs it; only native batch item rewriting clears providers.
func TestRewriteChatRequest_PreservesResolvedProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newRedirectService(t)
	checker := testCatalog()

	if chat, err := rewriteChatRequest(ctx, svc, checker, nil); err != nil || chat != nil {
		t.Fatalf("rewriteChatRequest(nil) = (%v, %v), want nil, nil", chat, err)
	}
	chat, err := rewriteChatRequest(ctx, svc, checker, &core.ChatRequest{Model: "fast"})
	if err != nil {
		t.Fatalf("rewriteChatRequest() error = %v", err)
	}
	if chat.Provider != "openai" || chat.Model != "gpt-4o" {
		t.Fatalf("rewriteChatRequest() selector = %q/%q, want openai/gpt-4o", chat.Provider, chat.Model)
	}
	if _, err := rewriteChatRequest(ctx, svc, checker, &core.ChatRequest{}); err == nil {
		t.Fatal("rewriteChatRequest(missing model) error = nil, want error")
	}
}
