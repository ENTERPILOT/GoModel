package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// fakePlugin is a configurable test plugin implementing prompt, response and
// stream hooks.
type fakePlugin struct {
	name     string
	kinds    []pluginapi.Kind
	mutates  bool
	schema   []pluginapi.Field
	initErr  error
	onPrompt func(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error)
	onResp   func(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error)
	policy   pluginapi.StreamPolicy
	onEvent  func(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error)
	onEnd    func(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error)
	config   json.RawMessage
	closed   bool
	mu       sync.Mutex
	calls    int
	lastHost pluginapi.Host
}

func (f *fakePlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: f.name, Version: "0.0.1", Kinds: f.kinds, Mutates: f.mutates, ConfigSchema: f.schema}
}

func (f *fakePlugin) Init(_ context.Context, config json.RawMessage, host pluginapi.Host) error {
	f.config = config
	f.lastHost = host
	return f.initErr
}

func (f *fakePlugin) Close(context.Context) error {
	f.closed = true
	return nil
}

func (f *fakePlugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.onPrompt == nil {
		return pluginapi.Allow(), nil
	}
	return f.onPrompt(ctx, x)
}

func (f *fakePlugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if f.onResp == nil {
		return pluginapi.Allow(), nil
	}
	return f.onResp(ctx, x)
}

func (f *fakePlugin) StreamPolicy() pluginapi.StreamPolicy { return f.policy }

func (f *fakePlugin) OnStreamEvent(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if f.onEvent == nil {
		return pluginapi.Pass(), nil
	}
	return f.onEvent(ctx, x, ev)
}

func (f *fakePlugin) OnStreamEnd(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if f.onEnd == nil {
		return pluginapi.Allow(), nil
	}
	return f.onEnd(ctx, x)
}

// promptOnly implements only the prompt hook.
type promptOnly struct{ fakePlugin }

func (p *promptOnly) OnResponse(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	panic("not a response hook")
}

func factoryOf(p pluginapi.Plugin) Factory {
	return func() pluginapi.Plugin { return p }
}

func newEntry(p pluginapi.Plugin) Entry {
	return Entry{Name: p.Manifest().Name, Manifest: p.Manifest(), Kinds: ImplementedKinds(p), Source: SourceBuiltin, Factory: factoryOf(p)}
}

func newTestInstance(p *fakePlugin, spec InstanceSpec) *Instance {
	if spec.Name == "" {
		spec.Name = p.name
	}
	inst, err := NewInstance(context.Background(), newEntry(p), spec, NewHost(HostDeps{}, HostInfo{PluginName: p.name, InstanceName: spec.Name}))
	if err != nil {
		panic(err)
	}
	return inst
}

func newExchange() *pluginapi.Exchange {
	return NewRequestState().NewExchange(context.Background(), pluginapi.Meta{RequestID: "req-1"})
}

func withPromptText(x *pluginapi.Exchange, text string) *pluginapi.Exchange {
	msg := pluginapi.TextMessage(pluginapi.RoleUser, text)
	msg.ID = "m0"
	x.Prompt = &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
	x.Prompt.Reset()
	return x
}

func sleepPrompt(d time.Duration) func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return func(ctx context.Context, _ *pluginapi.Exchange) (pluginapi.Decision, error) {
		select {
		case <-time.After(d):
			return pluginapi.Allow(), nil
		case <-ctx.Done():
			return pluginapi.Decision{}, ctx.Err()
		}
	}
}
