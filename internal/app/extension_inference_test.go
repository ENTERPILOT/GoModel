package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/pluginapi"
)

type plainRewriter struct{}

func (plainRewriter) Name() string { return "plain" }

func (plainRewriter) Rewrite(context.Context, ext.Input) (*ext.Result, error) { return nil, nil }

type inferenceAwareRewriter struct {
	plainRewriter
	name      string
	inference ext.Inference
}

func (r *inferenceAwareRewriter) Name() string { return r.name }

func (r *inferenceAwareRewriter) SetInference(inference ext.Inference) { r.inference = inference }

type recordingChat struct{ ctx context.Context }

func (c *recordingChat) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	c.ctx = ctx
	return &core.ChatResponse{Model: req.Model}, nil
}

var errTestDecide = errors.New("decide called")

type recordingDecider struct{ ctx context.Context }

func (d *recordingDecider) Decide(ctx context.Context, _ []byte) ([]byte, error) {
	d.ctx = ctx
	return nil, errTestDecide
}

func (*recordingDecider) DecisionModel(model string) bool { return model == "jev/jev-latest" }

func TestBindRewriterInference(t *testing.T) {
	bindRewriterInference(nil, plugins.HostDeps{})

	registry := &ext.Registry{}
	var typedNil *inferenceAwareRewriter
	registry.RegisterRewriter(typedNil)
	registry.RegisterRewriter(plainRewriter{})
	router := &inferenceAwareRewriter{name: "pro-smart-routing"}
	registry.RegisterRewriter(router)

	chat, decider := &recordingChat{}, &recordingDecider{}
	bindRewriterInference(registry, plugins.HostDeps{Chat: chat, Decisions: decider})
	require.NotNil(t, router.inference)

	ctx := core.WithEffectiveUserPath(context.Background(), "/acme")
	_, err := router.inference.Complete(ctx, pluginapi.InferenceRequest{Model: "openai/gpt-5-mini"})
	require.NoError(t, err)
	assert.Equal(t, "/acme/extensions/pro-smart-routing", core.UserPathFromContext(chat.ctx))
	assert.Equal(t, core.RequestOriginPlugin, core.GetRequestOrigin(chat.ctx))

	_, err = router.inference.Decide(ctx, pluginapi.DecisionRequest{
		Model:     "jev/jev-latest",
		Input:     []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "hi")},
		Questions: []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Instructions: "Is it hard?"}},
	})
	require.ErrorIs(t, err, errTestDecide)
	assert.Equal(t, "/acme/extensions/pro-smart-routing", core.UserPathFromContext(decider.ctx))
	assert.True(t, router.inference.DecisionModel("jev/jev-latest"))
	assert.False(t, router.inference.DecisionModel("openai/gpt-5-mini"))
}

func TestBindRewriterInferenceWithoutExecutors(t *testing.T) {
	registry := &ext.Registry{}
	router := &inferenceAwareRewriter{name: "router"}
	registry.RegisterRewriter(router)

	bindRewriterInference(registry, plugins.HostDeps{})
	require.NotNil(t, router.inference)
	_, err := router.inference.Complete(context.Background(), pluginapi.InferenceRequest{Model: "m"})
	require.ErrorIs(t, err, plugins.ErrInferenceUnavailable)
	_, err = router.inference.Decide(context.Background(), pluginapi.DecisionRequest{Model: "m"})
	require.ErrorIs(t, err, plugins.ErrDecisionsUnavailable)
}

// TestNewBindsRewriterInference checks that New wires inference-aware
// rewriters to the real internal executors: a call reaches model resolution
// instead of failing as unavailable.
func TestNewBindsRewriterInference(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "inference.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	loaded, err := config.Load()
	require.NoError(t, err)

	extensions := &ext.Registry{}
	router := &inferenceAwareRewriter{name: "router"}
	extensions.RegisterRewriter(router)

	ctx := context.Background()
	application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: extensions})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, application.Shutdown(ctx))
	})

	require.NotNil(t, router.inference)
	_, err = router.inference.Complete(ctx, pluginapi.InferenceRequest{
		Model:    "missing/model",
		Messages: []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "hi")},
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, plugins.ErrInferenceUnavailable)
	_, err = router.inference.Decide(ctx, pluginapi.DecisionRequest{
		Model:     "missing/model",
		Input:     []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "hi")},
		Questions: []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Instructions: "Is it hard?"}},
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, plugins.ErrDecisionsUnavailable)
}
