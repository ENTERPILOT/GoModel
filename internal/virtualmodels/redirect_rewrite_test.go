package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)

	inner := &chatExecMock{
		supported: map[string]bool{"openai/gpt-4o": true},
		resp:      &core.ChatResponse{ID: "chatcmpl_1", Model: "gpt-4o"},
	}
	executor := NewChatExecutor(inner, svc)

	resp, err := executor.ChatCompletion(ctx, &core.ChatRequest{Model: "fast"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "chatcmpl_1", resp.ID)
	require.NotNil(t, inner.captured)
	require.Equal(t, "gpt-4o", inner.captured.Model)
	require.Equal(t, "openai", inner.captured.Provider)
}

func TestChatExecutor_PassesThroughConcreteModel(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	inner := &chatExecMock{
		supported: map[string]bool{"openai/gpt-4o": true},
		resp:      &core.ChatResponse{ID: "chatcmpl_2"},
	}
	executor := NewChatExecutor(inner, svc)
	_, err := executor.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o", Provider: "openai"})
	require.NoError(t, err)
	require.NotNil(t, inner.captured)
	require.Equal(t, "gpt-4o", inner.captured.Model)
	require.Equal(t, "openai", inner.captured.Provider)
}

func TestChatExecutor_UnknownModelReturnsNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	inner := &chatExecMock{supported: map[string]bool{}}
	executor := NewChatExecutor(inner, svc)

	_, err := executor.ChatCompletion(context.Background(), &core.ChatRequest{Model: "missing"})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.NotNil(t, gatewayErr.Code)
	require.Equal(t, "model_not_found", *gatewayErr.Code)
	require.Nil(t, inner.captured)
}

// Translated request rewriting keeps the resolved provider because downstream
// routing still needs it; only native batch item rewriting clears providers.
func TestRewriteChatRequest_PreservesResolvedProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newRedirectService(t)
	checker := testCatalog()
	chat, err := rewriteChatRequest(ctx, svc, checker, nil)
	require.NoError(t, err)
	require.Nil(t, chat)

	chat, err = rewriteChatRequest(ctx, svc, checker, &core.ChatRequest{Model: "fast"})
	require.NoError(t, err)
	require.Equal(t, "openai", chat.Provider)
	require.Equal(t, "gpt-4o", chat.Model)
	_, err = rewriteChatRequest(ctx, svc, checker, &core.ChatRequest{})
	require.Error(t, err)
}
