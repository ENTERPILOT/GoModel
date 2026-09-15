package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
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
	elapsed := time.Since(start)
	require.LessOrEqual(t, elapsed, 250*time.Millisecond)
	require.ErrorIs(t, err, ErrAbandoned)
	require.Contains(t, err.Error(), "20ms timeout")
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
	require.ErrorIs(t, err, ErrAbandoned)
	if !errors.Is(err, context.Canceled) {
		require.ErrorContains(t, err, "canceled", "want abandoned by cancellation")
	}
}

func TestRunAbandonedHooks(t *testing.T) {
	timeout := InstanceSpec{Timeout: 20 * time.Millisecond, FailMode: FailOpen}
	late := func(x *pluginapi.Exchange) { x.Values.Set("late", true) }

	t.Run("abandoned mutator never fails open", func(t *testing.T) {
		mutator := &fakePlugin{name: "mut", mutates: true, onPrompt: stuckPrompt(100*time.Millisecond, late)}
		inst := newTestInstance(mutator, timeout)
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, 10}})
		require.NoError(t, err)

		_, err = chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
		pluginErr, ok := errors.AsType[*PluginError](err)
		require.True(t, ok)
		require.ErrorIs(t, pluginErr.Err, ErrAbandoned)
	})

	t.Run("abandoned reader fails open and its copy is dropped", func(t *testing.T) {
		reader := &fakePlugin{name: "reader", onPrompt: stuckPrompt(100*time.Millisecond, late)}
		quick := &fakePlugin{name: "quick", onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			x.Values.Set("quick", true)
			return pluginapi.Warn("w", "warned", nil), nil
		}}
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{newTestInstance(reader, timeout), 10}, {newTestInstance(quick, InstanceSpec{}), 10}})
		require.NoError(t, err)

		x := withPromptText(newExchange(), "x")
		outcome, err := chain.RunPrompt(context.Background(), x)
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionWarn, outcome.Decision.Action, "decision = %+v, want the quick reader's warn", outcome.Decision)
		_, ok := x.Values.Get("quick")
		require.True(t, ok)

		time.Sleep(150 * time.Millisecond)
		_, // let the abandoned reader finish writing its copy
			ok = x.Values.Get("late")
		require.False(t, ok)
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
	require.LessOrEqual(t, time.Since(start), 200*time.Millisecond)
	require.ErrorIs(t, err, ErrAbandoned)
	require.Contains(t, err.Error(), "init deadline")
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
	require.ErrorContains(t, err, "buffer stream policy needs OnResponse")
	require.True(t, buffered.closed)

	observing := &streamOnly{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform}}
	entry.Factory = func() pluginapi.Plugin { return observing }
	_, err = NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	require.NoError(t, err)
}

// slowInit ignores the init deadline, returns later, and reports Close.
type slowInit struct {
	delay  time.Duration
	closed chan struct{}
}

func (s *slowInit) Manifest() pluginapi.Manifest { return pluginapi.Manifest{Name: "slow"} }
func (s *slowInit) Init(context.Context, json.RawMessage, pluginapi.Host) error {
	time.Sleep(s.delay)
	return nil
}
func (s *slowInit) Close(context.Context) error { close(s.closed); return nil }

func TestNewInstanceClosesAbandonedInitOnceItReturns(t *testing.T) {
	previous := initTimeout
	initTimeout = 20 * time.Millisecond
	t.Cleanup(func() { initTimeout = previous })

	plugin := &slowInit{delay: 60 * time.Millisecond, closed: make(chan struct{})}
	entry := Entry{Name: "slow", Factory: func() pluginapi.Plugin { return plugin }}
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	require.ErrorIs(t, err, ErrAbandoned)

	select {
	case <-plugin.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned plugin was not closed after its Init returned")
	}
}

// stuckClose fails Init and then ignores its Close deadline; entered is
// closed when Close is called.
type stuckClose struct{ entered, release chan struct{} }

func (s *stuckClose) Manifest() pluginapi.Manifest { return pluginapi.Manifest{Name: "stuck-close"} }
func (s *stuckClose) Init(context.Context, json.RawMessage, pluginapi.Host) error {
	return errors.New("init failed")
}
func (s *stuckClose) Close(context.Context) error { close(s.entered); <-s.release; return nil }

func TestNewInstanceDoesNotWaitForAStuckCloseAfterFailedInit(t *testing.T) {
	previous := initTimeout
	initTimeout = 20 * time.Millisecond
	t.Cleanup(func() { initTimeout = previous })

	plugin := &stuckClose{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(plugin.release)
	entry := Entry{Name: "stuck-close", Factory: func() pluginapi.Plugin { return plugin }}
	start := time.Now()
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	require.ErrorContains(t, err, "init failed")
	require.LessOrEqual(t, time.Since(start), 500*time.Millisecond)

	select {
	case <-plugin.entered:
	case <-time.After(time.Second):
		t.Fatal("failed init did not call Close")
	}
}
