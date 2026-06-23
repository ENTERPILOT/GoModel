package virtualmodels

import (
	"context"
	"testing"

	"gomodel/internal/core"
)

// Translated request rewriting keeps the resolved provider because downstream
// routing still needs it; only native batch item rewriting clears providers.
func TestRewriteTranslatedRequests_PreservesResolvedProvider(t *testing.T) {
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
	if chat.Model != "gpt-4o" || chat.Provider != "openai" {
		t.Fatalf("rewriteChatRequest() selector = %q/%q, want openai/gpt-4o", chat.Provider, chat.Model)
	}
	if _, err := rewriteChatRequest(ctx, svc, checker, &core.ChatRequest{}); err == nil {
		t.Fatal("rewriteChatRequest(missing model) error = nil, want error")
	}

	if responses, err := rewriteResponsesRequest(ctx, svc, checker, nil); err != nil || responses != nil {
		t.Fatalf("rewriteResponsesRequest(nil) = (%v, %v), want nil, nil", responses, err)
	}
	responses, err := rewriteResponsesRequest(ctx, svc, checker, &core.ResponsesRequest{Model: "fast"})
	if err != nil {
		t.Fatalf("rewriteResponsesRequest() error = %v", err)
	}
	if responses.Model != "gpt-4o" || responses.Provider != "openai" {
		t.Fatalf("rewriteResponsesRequest() selector = %q/%q, want openai/gpt-4o", responses.Provider, responses.Model)
	}
	if _, err := rewriteResponsesRequest(ctx, svc, checker, &core.ResponsesRequest{}); err == nil {
		t.Fatal("rewriteResponsesRequest(missing model) error = nil, want error")
	}

	if embeddings, err := rewriteEmbeddingRequest(ctx, svc, checker, nil); err != nil || embeddings != nil {
		t.Fatalf("rewriteEmbeddingRequest(nil) = (%v, %v), want nil, nil", embeddings, err)
	}
	embeddings, err := rewriteEmbeddingRequest(ctx, svc, checker, &core.EmbeddingRequest{Model: "fast"})
	if err != nil {
		t.Fatalf("rewriteEmbeddingRequest() error = %v", err)
	}
	if embeddings.Model != "gpt-4o" || embeddings.Provider != "openai" {
		t.Fatalf("rewriteEmbeddingRequest() selector = %q/%q, want openai/gpt-4o", embeddings.Provider, embeddings.Model)
	}
	if _, err := rewriteEmbeddingRequest(ctx, svc, checker, &core.EmbeddingRequest{}); err == nil {
		t.Fatal("rewriteEmbeddingRequest(missing model) error = nil, want error")
	}
}
