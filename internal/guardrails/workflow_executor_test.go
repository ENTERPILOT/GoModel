package guardrails

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
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
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hello"}}}

	ctx, state := plugins.WithRequestState(plugins.WithPromptEditCapture(context.Background()))
	applied, err := processGuardedChat(ctx, chain, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := applied.Messages[0].Content; got != "hello one two" {
		t.Fatalf("applied content = %q", got)
	}
	edits := state.PromptEdits()
	if len(edits) != 2 || edits[0].Instance != "first" || edits[1].Instance != "second" {
		t.Fatalf("edits = %+v", edits)
	}
	for i, want := range []string{"hello one", "hello one two"} {
		step, err := edits[i].Apply()
		if err != nil {
			t.Fatal(err)
		}
		if got := step.(*core.ChatRequest).Messages[0].Content; got != want {
			t.Errorf("step %d content = %q, want %q", i+1, got, want)
		}
	}
	if req.Messages[0].Content != "hello" {
		t.Errorf("the original request was edited in place: %q", req.Messages[0].Content)
	}

	ctx, state = plugins.WithRequestState(context.Background())
	if _, err := processGuardedChat(ctx, chain, req); err != nil {
		t.Fatal(err)
	}
	if edits := state.PromptEdits(); len(edits) != 0 {
		t.Errorf("edits kept without capture: %+v", edits)
	}
}
