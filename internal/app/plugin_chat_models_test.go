package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/server"
)

// countingChatProvider counts the chat completions that reach it.
type countingChatProvider struct {
	runtimeRefreshMockProvider
	calls int
}

func (p *countingChatProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	p.calls++
	return &core.ChatResponse{Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}}}, nil
}

// newChatModelsRouter lists a Jev decision model and an OpenAI chat model,
// as their providers catalog them.
func newChatModelsRouter(t *testing.T) (*providers.Router, *countingChatProvider, *countingChatProvider) {
	t.Helper()
	jev := &countingChatProvider{models: &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "jev-latest", Object: "model", Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryUtility}}},
	}}}
	openai := &countingChatProvider{models: &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "gpt-6-luna", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"chat", "responses"}}},
	}}}
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(jev, "jev", "jev")
	registry.RegisterProviderWithNameAndType(openai, "openai", "openai")
	require.NoError(t, registry.Initialize(context.Background()))
	router, err := providers.NewRouter(registry)
	require.NoError(t, err)
	return router, jev, openai
}

// A guardrail's model field is checked as named when the guardrail is saved:
// a decision model is refused, a chat or uncatalogued model passes, and a
// virtual model is left to chatModelGuard, since resolving it here would
// pick a target and advance its load balancing.
func TestPluginChatModels_ChecksTheModelAsNamed(t *testing.T) {
	router, _, _ := newChatModelsRouter(t)
	checker := pluginChatModels{router: router}

	assert.Contains(t, checker.ChatUnsupportedReason(context.Background(), "jev/jev-latest"), "is a decision model")
	assert.Empty(t, checker.ChatUnsupportedReason(context.Background(), "openai/gpt-6-luna"))
	assert.Empty(t, checker.ChatUnsupportedReason(context.Background(), "nowhere/unknown"))
	assert.Empty(t, checker.ChatUnsupportedReason(context.Background(), "decider"), "a virtual model is not resolved at save time")
}

// The guard sees the concrete model a plugin call resolved to and refuses a
// decision model before its provider is called, while chat models pass.
func TestChatModelGuard_RefusesResolvedModelsThatCannotChat(t *testing.T) {
	router, jev, openai := newChatModelsRouter(t)
	guard := chatModelGuard{Router: router}

	_, err := guard.ChatCompletion(context.Background(), &core.ChatRequest{Provider: "jev", Model: "jev-latest"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `model "jev/jev-latest" is a decision model`)
	assert.Contains(t, err.Error(), "choose a chat model")
	assert.Zero(t, jev.calls)

	_, err = guard.ChatCompletion(context.Background(), &core.ChatRequest{Provider: "openai", Model: "gpt-6-luna"})
	require.NoError(t, err)
	assert.Equal(t, 1, openai.calls)
}

// judgeAlias resolves the virtual model "judge" to Jev's decision model, as a
// redirect would.
type judgeAlias struct{}

func (judgeAlias) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.Model != "judge" {
		return core.ModelSelector{Provider: requested.ProviderHint, Model: requested.Model}, false, nil
	}
	return core.ModelSelector{Provider: "jev", Model: "jev-latest"}, true, nil
}

// Plugin calls in production run through the internal chat executor, which
// resolves virtual models itself; over the guard, a judge whose virtual
// model resolves to a decision model is refused before Jev is called.
func TestChatModelGuard_GuardsTheInternalChatExecutor(t *testing.T) {
	router, jev, _ := newChatModelsRouter(t)

	unguarded := server.NewInternalChatCompletionExecutor(router, server.InternalChatCompletionExecutorConfig{ModelResolver: judgeAlias{}})
	_, err := unguarded.ChatCompletion(context.Background(), &core.ChatRequest{Model: "judge"})
	require.NoError(t, err)
	require.Equal(t, 1, jev.calls, "without the guard the decision model is called")

	guarded := server.NewInternalChatCompletionExecutor(chatModelGuard{Router: router}, server.InternalChatCompletionExecutorConfig{ModelResolver: judgeAlias{}})
	_, err = guarded.ChatCompletion(context.Background(), &core.ChatRequest{Model: "judge"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a decision model")
	assert.Equal(t, 1, jev.calls)
}
