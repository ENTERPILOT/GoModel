package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// stuckPrompt ignores its context and only returns after d.
func stuckPrompt(d time.Duration, then func(x *pluginapi.Exchange)) func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		time.Sleep(d)
		if then != nil {
			then(x)
		}
		return pluginapi.Allow(), nil
	}
}

func TestCallReturnsAtDeadlineForHookIgnoringContext(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "stuck", onPrompt: stuckPrompt(500*time.Millisecond, nil)}, InstanceSpec{Timeout: 20 * time.Millisecond})
	start := time.Now()
	_, err := Call(context.Background(), inst, func(ctx context.Context) (pluginapi.Decision, error) {
		return inst.Plugin.(pluginapi.PromptHook).OnPrompt(ctx, newExchange())
	})
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("Call took %s, want to return at the 20ms deadline", elapsed)
	}
	if !errors.Is(err, ErrAbandoned) || !strings.Contains(err.Error(), "20ms timeout") {
		t.Fatalf("error = %v, want ErrAbandoned with the timeout", err)
	}
}

func TestCallReturnsWhenRequestEnds(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "stuck"}, InstanceSpec{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := Call(ctx, inst, func(context.Context) (pluginapi.Decision, error) {
		time.Sleep(500 * time.Millisecond)
		return pluginapi.Allow(), nil
	})
	if !errors.Is(err, ErrAbandoned) || !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error = %v, want abandoned by cancellation", err)
	}
}

func TestRunAbandonedHooks(t *testing.T) {
	timeout := InstanceSpec{Timeout: 20 * time.Millisecond, FailMode: FailOpen}
	late := func(x *pluginapi.Exchange) { x.Values.Set("late", true) }

	t.Run("abandoned mutator never fails open", func(t *testing.T) {
		mutator := &fakePlugin{name: "mut", mutates: true, onPrompt: stuckPrompt(100*time.Millisecond, late)}
		inst := newTestInstance(mutator, timeout)
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, 10}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
		pluginErr, ok := errors.AsType[*PluginError](err)
		if !ok || !errors.Is(pluginErr.Err, ErrAbandoned) {
			t.Fatalf("error = %v, want PluginError wrapping ErrAbandoned despite fail_open", err)
		}
	})

	t.Run("abandoned reader fails open and its copy is dropped", func(t *testing.T) {
		reader := &fakePlugin{name: "reader", onPrompt: stuckPrompt(100*time.Millisecond, late)}
		quick := &fakePlugin{name: "quick", onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			x.Values.Set("quick", true)
			return pluginapi.Warn("w", "warned", nil), nil
		}}
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{newTestInstance(reader, timeout), 10}, {newTestInstance(quick, InstanceSpec{}), 10}})
		if err != nil {
			t.Fatal(err)
		}
		x := withPromptText(newExchange(), "x")
		outcome, err := chain.RunPrompt(context.Background(), x)
		if err != nil {
			t.Fatalf("error = %v, want fail open", err)
		}
		if outcome.Decision.Action != pluginapi.ActionWarn {
			t.Fatalf("decision = %+v, want the quick reader's warn", outcome.Decision)
		}
		if _, ok := x.Values.Get("quick"); !ok {
			t.Fatal("quick reader's values were not merged")
		}
		time.Sleep(150 * time.Millisecond) // let the abandoned reader finish writing its copy
		if _, ok := x.Values.Get("late"); ok {
			t.Fatal("abandoned reader's values leaked into the exchange")
		}
	})
}

type stuckInit struct{ fakePlugin }

func (*stuckInit) Init(context.Context, json.RawMessage, pluginapi.Host) error {
	time.Sleep(300 * time.Millisecond)
	return nil
}

func TestNewInstanceAbandonsInitAtDeadline(t *testing.T) {
	previous := initTimeout
	initTimeout = 20 * time.Millisecond
	t.Cleanup(func() { initTimeout = previous })

	entry := Entry{Name: "stuck", Factory: func() pluginapi.Plugin { return &stuckInit{} }}
	start := time.Now()
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("NewInstance waited past the init deadline")
	}
	if !errors.Is(err, ErrAbandoned) || !strings.Contains(err.Error(), "init deadline") {
		t.Fatalf("error = %v, want ErrAbandoned at the init deadline", err)
	}
}

// streamOnly implements the stream hook but not the response hook.
type streamOnly struct {
	policy pluginapi.StreamPolicy
	closed bool
}

func (s *streamOnly) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "stream-only", Version: "0.0.1", Kinds: []pluginapi.Kind{pluginapi.KindStream}}
}
func (*streamOnly) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (s *streamOnly) Close(context.Context) error                               { s.closed = true; return nil }
func (s *streamOnly) StreamPolicy() pluginapi.StreamPolicy                      { return s.policy }
func (*streamOnly) OnStreamEvent(context.Context, *pluginapi.Exchange, *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	return pluginapi.Pass(), nil
}
func (*streamOnly) OnStreamEnd(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func TestNewInstanceRejectsBufferPolicyWithoutResponseHook(t *testing.T) {
	buffered := &streamOnly{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}}
	entry := Entry{Name: "stream-only", Manifest: buffered.Manifest(), Kinds: ImplementedKinds(buffered), Factory: func() pluginapi.Plugin { return buffered }}
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	if err == nil || !strings.Contains(err.Error(), "buffer stream policy needs OnResponse") {
		t.Fatalf("error = %v, want buffer policy rejection", err)
	}
	if !buffered.closed {
		t.Fatal("rejected plugin was not closed")
	}

	observing := &streamOnly{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform}}
	entry.Factory = func() pluginapi.Plugin { return observing }
	if _, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{})); err != nil {
		t.Fatalf("transform policy rejected: %v", err)
	}
}
