package guardrails

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// runawayPlugin is a mutating prompt plugin that ignores its context and
// keeps editing the prompt until the test ends.
type runawayPlugin struct{ stop chan struct{} }

func (p *runawayPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "runaway", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}, Mutates: true}
}
func (p *runawayPlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (p *runawayPlugin) Close(context.Context) error                                 { return nil }
func (p *runawayPlugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	for {
		select {
		case <-p.stop:
			return pluginapi.Allow(), nil
		default:
			_ = x.Prompt.SetText("m0", 0, "still editing")
		}
	}
}

// The prompt phase must not read the prompt or exchange once a mutator was
// abandoned: the hook may still be writing them. Meaningful under -race.
func TestPromptRunDoesNotReadAbandonedExchange(t *testing.T) {
	plugin := &runawayPlugin{stop: make(chan struct{})}
	defer close(plugin.stop)
	entry := plugins.Entry{Name: "runaway", Manifest: plugin.Manifest(), Kinds: plugins.ImplementedKinds(plugin), Source: plugins.SourceBuiltin, Factory: func() pluginapi.Plugin { return plugin }}
	host := plugins.NewHost(plugins.HostDeps{}, plugins.HostInfo{PluginName: "runaway", InstanceName: "runaway"})
	inst, err := plugins.NewInstance(context.Background(), entry, plugins.InstanceSpec{Name: "runaway", Timeout: 20 * time.Millisecond}, host)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := plugins.BuildChain(pluginapi.KindPrompt, []plugins.Ref{{Instance: inst, Step: 1}})
	if err != nil {
		t.Fatal(err)
	}
	msg := pluginapi.TextMessage(pluginapi.RoleUser, "hello")
	msg.ID = "m0"
	prompt := &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
	prompt.Reset()

	edited, err := newPromptRun(context.Background(), chain).run(context.Background(), prompt, nil)
	if err == nil || edited {
		t.Fatalf("run = edited %v, err %v; want a failure and no edit", edited, err)
	}
}
