package guardrails

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appendPlugin appends its text to the last user message.
type appendPlugin struct{ text string }

func (p *appendPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "append", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}, Mutates: true}
}
func (p *appendPlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (p *appendPlugin) Close(context.Context) error                                 { return nil }
func (p *appendPlugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	last := x.Prompt.LastUser()
	return pluginapi.Allow(), x.Prompt.SetText(last.ID, 0, last.Text()+" "+p.text)
}

func appendInstance(t *testing.T, name, text string) *plugins.Instance {
	t.Helper()
	plugin := &appendPlugin{text: text}
	entry := plugins.Entry{Name: "append", Manifest: plugin.Manifest(), Kinds: plugins.ImplementedKinds(plugin), Source: plugins.SourceBuiltin, Factory: func() pluginapi.Plugin { return plugin }}
	host := plugins.NewHost(plugins.HostDeps{}, plugins.HostInfo{PluginName: "append", InstanceName: name})
	inst, err := plugins.NewInstance(context.Background(), entry, plugins.InstanceSpec{Name: name, Timeout: time.Second}, host)
	require.NoError(t, err)

	return inst
}

// With prompt-edit capture on, every editing step leaves a PromptEdit whose
// Apply yields the request as that step left it, each built on the previous
// step's edit; without capture nothing is kept.
func TestProcessGuardedChatKeepsOneSnapshotPerEditingStep(t *testing.T) {
	chain, err := plugins.BuildChain(pluginapi.KindPrompt, []plugins.Ref{
		{Instance: appendInstance(t, "first", "one"), Step: 1},
		{Instance: appendInstance(t, "second", "two"), Step: 2},
	})
	require.NoError(t, err)

	req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hello"}}}

	ctx, state := plugins.WithRequestState(plugins.WithPromptEditCapture(context.Background()))
	applied, err := processGuardedChat(ctx, chain, req)
	require.NoError(t, err)
	got := applied.Messages[0].Content
	require.Equal(t, "hello one two", got)

	edits := state.PromptEdits()
	require.Len(t, edits, 2)
	require.Equal(t, "first", edits[0].Instance)
	require.Equal(t, "second", edits[1].Instance)

	for i, want := range []string{"hello one", "hello one two"} {
		step, err := edits[i].Apply()
		require.NoError(t, err)
		got := step.(*core.ChatRequest).Messages[0].Content
		assert.Equal(t, want, got)
	}
	assert.Equal(t, "hello", req.Messages[0].Content)

	ctx, state = plugins.WithRequestState(context.Background())
	_, err = processGuardedChat(ctx, chain, req)
	require.NoError(t, err)
	edits = state.PromptEdits()
	assert.Empty(t, edits)
}
