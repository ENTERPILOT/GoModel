package virtualmodels

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)

	return svc
}

// requireGatewayError asserts the gateway error contract while returning the
// typed error for any additional test-specific checks.
func requireGatewayError(t *testing.T, err error, wantType core.ErrorType, wantCode string) *core.GatewayError {
	t.Helper()
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, wantType, gatewayErr.Type)

	if wantCode != "" {
		require.NotNil(t, gatewayErr.Code, "error code = nil, want %q", wantCode)
		require.Equal(t, wantCode, *gatewayErr.Code)
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
	require.NoError(t, err)

	var out core.ChatRequest
	err = json.Unmarshal(body, &out)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", out.Model)
	require.Empty(t, out.Provider)
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
	require.ErrorIs(t, err, denied)
	require.Equal(t, "openai", validated.Provider)
	require.Equal(t, "gpt-4o", validated.Model)
}

// A malformed / unsupported batch item is rejected rather than silently passed.
func TestRewriteBatchItem_UnsupportedItem(t *testing.T) {
	t.Parallel()
	decoded := &core.DecodedBatchItemRequest{Endpoint: "/v1/unknown", Request: "not a request"}
	_, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "", decoded, nil)
	require.Error(t, err)

	_ = requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "")
}

// Native batch is single-provider: a resolved target whose provider differs from
// the batch provider is rejected.
func TestRewriteBatchItem_RejectsCrossProviderBatch(t *testing.T) {
	t.Parallel()
	_, err := rewriteBatchItem(context.Background(), newRedirectService(t), testCatalog(), "anthropic", decodedChatItem("fast", ""), nil)
	require.Error(t, err)

	gatewayErr := requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "")
	require.Contains(t, gatewayErr.Message, "single provider per batch")
}

// BatchPreparer.validateAccess enforces the access policy; a nil-service preparer
// (provider-wrapper parity) never blocks.
func TestBatchPreparerValidateAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	enabledSvc := newTestService(t)
	err := enabledSvc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true})
	require.NoError(t, err)
	err = NewBatchPreparer(nil, enabledSvc).validateAccess(ctx, selector)
	require.NoError(t, err)

	svc := newTestService(t)
	err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: false})
	require.NoError(t, err)

	err = NewBatchPreparer(nil, svc).validateAccess(ctx, selector)
	require.Error(t, err)

	_ = requireGatewayError(t, err, core.ErrorTypeInvalidRequest, "model_access_denied")
	err = (&BatchPreparer{}).validateAccess(ctx, selector)
	require.NoError(t, err)
}
