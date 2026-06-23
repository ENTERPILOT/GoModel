package virtualmodels

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gomodel/internal/core"
)

// decodedChatItem builds a decoded chat batch request for per-item rewrite tests.
func decodedChatItem(model, provider string) *core.DecodedBatchItemRequest {
	return &core.DecodedBatchItemRequest{
		Endpoint: "/v1/chat/completions",
		Request:  &core.ChatRequest{Model: model, Provider: provider},
	}
}

// newRedirectService creates a service with the "fast" redirect used by batch
// rewrite tests.
func newRedirectService(t *testing.T) *Service {
	t.Helper()
	svc := newTestService(t)
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	return svc
}

// requireGatewayError asserts the gateway error contract while returning the
// typed error for any additional test-specific checks.
func requireGatewayError(t *testing.T, err error, wantType core.ErrorType, wantCode string) *core.GatewayError {
	t.Helper()
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != wantType {
		t.Fatalf("error type = %q, want %q", gatewayErr.Type, wantType)
	}
	if wantCode != "" {
		if gatewayErr.Code == nil {
			t.Fatalf("error code = nil, want %q", wantCode)
		}
		if *gatewayErr.Code != wantCode {
			t.Fatalf("error code = %q, want %q", *gatewayErr.Code, wantCode)
		}
	}
	return gatewayErr
}

// Provider-wrapper-style call: nil validation, redirect rewritten and the
// per-item provider cleared before upstream submission.
func TestRewriteBatchItem_RewritesAndClearsProvider(t *testing.T) {
	t.Parallel()
	// No explicit provider on the item, so the "fast" redirect applies; the
	// resolved target (openai/gpt-4o) is written as the model with the provider
	// cleared for upstream.
	body, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "", decodedChatItem("fast", ""), nil)
	if err != nil {
		t.Fatalf("rewriteBatchItem() error = %v", err)
	}
	var out core.ChatRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if out.Model != "gpt-4o" {
		t.Fatalf("rewritten model = %q, want gpt-4o (redirect resolved)", out.Model)
	}
	if out.Provider != "" {
		t.Fatalf("rewritten provider = %q, want empty (cleared for upstream)", out.Provider)
	}
}

// Server-side preparer call: the validate hook denies an unauthorized resolved
// selector and the error is surfaced.
func TestRewriteBatchItem_ValidateRejectsUnauthorized(t *testing.T) {
	t.Parallel()
	denied := errors.New("denied")
	var validated core.ModelSelector
	_, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "", decodedChatItem("fast", ""),
		func(_ context.Context, resolved core.ModelSelector) error {
			validated = resolved
			return denied
		})
	if !errors.Is(err, denied) {
		t.Fatalf("rewriteBatchItem() error = %v, want denied", err)
	}
	if validated.Provider != "openai" || validated.Model != "gpt-4o" {
		t.Fatalf("validated selector = %q/%q, want openai/gpt-4o", validated.Provider, validated.Model)
	}
}

// A malformed / unsupported batch item is rejected rather than silently passed.
func TestRewriteBatchItem_UnsupportedItem(t *testing.T) {
	t.Parallel()
	decoded := &core.DecodedBatchItemRequest{Endpoint: "/v1/unknown", Request: "not a request"}
	_, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "", decoded, nil)
	if err == nil {
		t.Fatal("rewriteBatchItem(unsupported item) error = nil, want error")
	}
	_ = requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "")
}

// Native batch is single-provider: a resolved target whose provider differs from
// the batch provider is rejected.
func TestRewriteBatchItem_RejectsCrossProviderBatch(t *testing.T) {
	t.Parallel()
	_, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "anthropic", decodedChatItem("fast", ""), nil)
	if err == nil {
		t.Fatal("rewriteBatchItem(cross-provider batch) error = nil, want single-provider-per-batch error")
	}
	gatewayErr := requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "")
	if !strings.Contains(gatewayErr.Message, "single provider per batch") {
		t.Fatalf("rewriteBatchItem(cross-provider batch) error = %q, want single-provider reason", gatewayErr.Message)
	}
}

// BatchPreparer.validateAccess enforces the access policy; a nil-service preparer
// (provider-wrapper parity) never blocks.
func TestBatchPreparerValidateAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	enabledSvc := newTestService(t)
	if err := enabledSvc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true}); err != nil {
		t.Fatalf("Upsert(enabled policy) error = %v", err)
	}
	if err := NewBatchPreparer(nil, enabledSvc).validateAccess(ctx, selector); err != nil {
		t.Fatalf("validateAccess(enabled model) error = %v, want nil", err)
	}

	svc := newTestService(t)
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: false}); err != nil {
		t.Fatalf("Upsert(disabled policy) error = %v", err)
	}

	err := NewBatchPreparer(nil, svc).validateAccess(ctx, selector)
	if err == nil {
		t.Fatal("validateAccess(disabled model) error = nil, want denied")
	}
	_ = requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "model_access_denied")

	if err := (&BatchPreparer{}).validateAccess(ctx, selector); err != nil {
		t.Fatalf("validateAccess(nil service) error = %v, want nil", err)
	}
}
