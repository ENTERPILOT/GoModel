package plugins

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPromptOrderingAndEdits(t *testing.T) {
	var order []string
	mk := func(name string, mutates bool, d func(*pluginapi.Exchange) pluginapi.Decision) *Instance {
		return newTestInstance(&fakePlugin{name: name, mutates: mutates, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			order = append(order, name)
			if d == nil {
				return pluginapi.Allow(), nil
			}
			return d(x), nil
		}}, InstanceSpec{})
	}
	editor := mk("editor", true, func(x *pluginapi.Exchange) pluginapi.Decision {
		_ = x.Prompt.SetText("m0", 0, "edited")
		x.Values.Set("editor.ran", true)
		return pluginapi.Allow()
	})
	checker := mk("checker", false, func(x *pluginapi.Exchange) pluginapi.Decision {
		if x.Prompt.Messages[0].Text() != "edited" {
			return pluginapi.Block(0, "not_edited", "expected edited text")
		}
		return pluginapi.Warn("looks_ok", "fine", map[string]any{"n": 1})
	})
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{checker, 20}, {editor, 10}})
	require.NoError(t, err)

	x := withPromptText(newExchange(), "hello")
	outcome, err := chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Len(t, order, 2)
	require.Equal(t, "editor", order[0])
	require.Equal(t, "checker", order[1])
	require.Equal(t, pluginapi.ActionWarn, outcome.Decision.Action)
	require.Equal(t, "checker", outcome.Instance)
	require.Len(t, outcome.Records, 2)
	require.Equal(t, "editor", outcome.Records[0].Instance)
	v, _ := x.Values.Get("editor.ran")
	require.Equal(t, true, v)
}

// Edited is per instance: a mutator that edits is marked, a later mutator
// that leaves the prompt alone is not, whatever an earlier step changed.
func TestRunMarksOnlyTheInstanceThatEdited(t *testing.T) {
	mk := func(name string, mutates bool, edit func(*pluginapi.Exchange)) *Instance {
		return newTestInstance(&fakePlugin{name: name, mutates: mutates, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			if edit != nil {
				edit(x)
			}
			return pluginapi.Allow(), nil
		}}, InstanceSpec{})
	}
	editor := mk("editor", true, func(x *pluginapi.Exchange) { _ = x.Prompt.SetText("m0", 0, "edited") })
	noop := mk("noop", true, nil)
	again := mk("again", true, func(x *pluginapi.Exchange) { _ = x.Prompt.SetText("m0", 0, "edited twice") })
	reader := mk("reader", false, nil)
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{editor, 10}, {noop, 20}, {reader, 20}, {again, 30}})
	require.NoError(t, err)

	outcome, err := chain.RunPrompt(context.Background(), withPromptText(newExchange(), "hello"))
	require.NoError(t, err)

	want := map[string]bool{"editor": true, "noop": false, "reader": false, "again": true}
	require.Len(t, outcome.Records, len(want))

	seen := make(map[string]bool, len(want))
	for _, record := range outcome.Records {
		expected, ok := want[record.Instance]
		if !assert.True(t, ok, "unexpected record %q", record.Instance) {
			continue
		}
		seen[record.Instance] = true
		assert.Equal(t, expected, record.Edited, "%s: edited", record.Instance)
	}
	for instance := range want {
		assert.True(t, seen[instance], "missing record %q", instance)
	}
}

// The observer sees each edit right after its step, with the prompt as that
// step left it, so a later step's edit builds on the earlier one.
func TestRunPromptObservedReportsEachEditInOrder(t *testing.T) {
	mk := func(name string, mutates bool, edit func(*pluginapi.Exchange)) *Instance {
		return newTestInstance(&fakePlugin{name: name, mutates: mutates, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			if edit != nil {
				edit(x)
			}
			return pluginapi.Allow(), nil
		}}, InstanceSpec{})
	}
	first := mk("first", true, func(x *pluginapi.Exchange) { _ = x.Prompt.SetText("m0", 0, x.Prompt.Messages[0].Text()+" one") })
	second := mk("second", true, func(x *pluginapi.Exchange) { _ = x.Prompt.SetText("m0", 0, x.Prompt.Messages[0].Text()+" two") })
	noop := mk("noop", true, nil)
	reader := mk("reader", false, nil)
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{first, 10}, {reader, 10}, {noop, 20}, {second, 30}})
	require.NoError(t, err)

	var seen []string
	observe := func(instance string, x *pluginapi.Exchange) {
		seen = append(seen, instance+": "+x.Prompt.Messages[0].Text())
	}
	_, err = chain.RunPromptObserved(context.Background(), withPromptText(newExchange(), "hello"), observe)
	require.NoError(t, err)

	want := []string{"first: hello one", "second: hello one two"}
	require.Len(t, seen, len(want))
	require.Equal(t, want[0], seen[0])
	require.Equal(t, want[1], seen[1])

	// A mutator that edits and then fails closed is observed too, before
	// the run returns its error.
	failing := newTestInstance(&fakePlugin{name: "failing", mutates: true, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		_ = x.Prompt.SetText("m0", 0, "edited then failed")
		return pluginapi.Allow(), errors.New("boom")
	}}, InstanceSpec{FailMode: FailClosed})
	chain, err = BuildChain(pluginapi.KindPrompt, []Ref{{failing, 10}, {second, 20}})
	require.NoError(t, err)

	seen = nil
	_, err = chain.RunPromptObserved(context.Background(), withPromptText(newExchange(), "hello"), observe)
	require.Error(t, err)
	require.Len(t, seen, 1)
	require.Equal(t, "failing: edited then failed", seen[0])
}

func TestRunReadersConcurrentAndMergeSeverity(t *testing.T) {
	var inFlight, maxInFlight int32
	reader := func(name string, d pluginapi.Decision) *Instance {
		return newTestInstance(&fakePlugin{name: name, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				current := atomic.LoadInt32(&maxInFlight)
				if n <= current || atomic.CompareAndSwapInt32(&maxInFlight, current, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			x.Values.Set(name, true)
			x.Headers.Response.Add("X-"+name, "1")
			return d, nil
		}}, InstanceSpec{})
	}
	never := newTestInstance(&fakePlugin{name: "never", onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
		t.Error("step after a blocking step must not run")
		return pluginapi.Allow(), nil
	}}, InstanceSpec{})
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{
		{reader("warn", pluginapi.Warn("w", "", nil)), 10},
		{reader("respond", pluginapi.Respond("no")), 10},
		{reader("block", pluginapi.Block(451, "policy", "blocked")), 10},
		{never, 20},
	})
	require.NoError(t, err)

	x := withPromptText(newExchange(), "hi")
	outcome, err := chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)
	require.GreaterOrEqual(t, atomic.LoadInt32(&maxInFlight), int32(2), "readers did not run concurrently (max in flight %d)", maxInFlight)
	require.Equal(t, pluginapi.ActionBlock, outcome.Decision.Action)
	require.Equal(t, 451, outcome.Decision.Status)
	require.Equal(t, "block", outcome.Instance)

	for _, name := range []string{"warn", "respond", "block"} {
		_, ok := x.Values.Get(name)
		require.True(t, ok, "value %s not merged back", name)
		require.Equal(t, "1", x.Headers.Response.Get("X-"+name), "header X-%s not merged back", name)
	}
	require.Len(t, outcome.Records, 3)
}

func TestRunFailModesTimeoutsAndPanics(t *testing.T) {
	tests := []struct {
		name     string
		plugin   *fakePlugin
		spec     InstanceSpec
		wantErr  bool
		wantWarn bool
	}{
		{
			name: "error fails closed by default",
			plugin: &fakePlugin{name: "err", onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
				return pluginapi.Decision{}, errFake
			}},
			wantErr: true,
		},
		{
			name: "error fails open when configured",
			plugin: &fakePlugin{name: "err-open", onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
				return pluginapi.Decision{}, errFake
			}},
			spec: InstanceSpec{FailMode: FailOpen},
		},
		{
			name:    "panic is recovered",
			plugin:  &fakePlugin{name: "panic", onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) { panic("boom") }},
			wantErr: true,
		},
		{
			name:    "timeout fails closed",
			plugin:  &fakePlugin{name: "slow", onPrompt: sleepPrompt(200 * time.Millisecond)},
			spec:    InstanceSpec{Timeout: 20 * time.Millisecond},
			wantErr: true,
		},
		{
			name:   "timeout fails open",
			plugin: &fakePlugin{name: "slow-open", onPrompt: sleepPrompt(200 * time.Millisecond)},
			spec:   InstanceSpec{Timeout: 20 * time.Millisecond, FailMode: FailOpen},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := newTestInstance(tt.plugin, tt.spec)
			after := newTestInstance(&fakePlugin{name: "after"}, InstanceSpec{})
			chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, 10}, {after, 20}})
			require.NoError(t, err)

			outcome, err := chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
			if tt.wantErr {
				var pluginErr *PluginError
				require.ErrorAs(t, err, &pluginErr)
				require.Equal(t, tt.plugin.name, pluginErr.Instance)
				require.Len(t, outcome.Records, 1)
				require.Error(t, outcome.Records[0].Err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, pluginapi.ActionAllow, outcome.Decision.Action)
			require.Len(t, outcome.Records, 2)
			require.Error(t, outcome.Records[0].Err, "outcome = %+v", outcome)
		})
	}
}

func TestRunResponseAndStreamEnd(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "r",
		onResp: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			return pluginapi.Decision{Action: pluginapi.ActionRespond}, nil
		},
		onEnd: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
			return pluginapi.Block(0, "c", "m"), nil
		},
	}, InstanceSpec{})
	chain, err := BuildChain(pluginapi.KindResponse, []Ref{{inst, 1}})
	require.NoError(t, err)

	outcome, err := chain.RunResponse(context.Background(), newExchange())
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionRespond, outcome.Decision.Action)
	require.NotNil(t, outcome.Decision.Response, "RunResponse = %+v, %v (respond without completion must be normalized)", outcome, err)

	stream, err := BuildChain(pluginapi.KindStream, []Ref{{inst, 1}})
	require.NoError(t, err)

	outcome, err = stream.RunStreamEnd(context.Background(), newExchange())
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionBlock, outcome.Decision.Action)

	var empty *Chain
	outcome, err = empty.RunPrompt(context.Background(), newExchange())
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, outcome.Decision.Action)
}

func TestDecisionHelpers(t *testing.T) {
	require.Equal(t, pluginapi.ActionWarn, MergeDecision(pluginapi.Warn("a", "", nil), pluginapi.Allow()).Action)

	blocked := BlockError(pluginapi.Block(0, "", ""), 400)
	require.Equal(t, 400, blocked.HTTPStatusCode())
	require.NotNil(t, blocked.Code)
	require.Equal(t, CodeBlocked, *blocked.Code)
	require.NotEmpty(t, blocked.Message)

	custom := BlockError(pluginapi.Block(502, "x", "y"), 400)
	require.Equal(t, 502, custom.HTTPStatusCode())
	require.Equal(t, "x", *custom.Code)
	require.Equal(t, "y", custom.Message)

	failure := FailureError(errFake)
	require.Equal(t, 500, failure.HTTPStatusCode())
	require.Equal(t, CodePluginFailure, *failure.Code)
	require.NotEqual(t, errFake.Error(), failure.Message, "FailureError = %+v", failure)
	require.Equal(t, "warn; code=pii", WarnHeaderValue(pluginapi.Warn("pii", "", nil)))
	require.Equal(t, 502, DefaultBlockStatus(pluginapi.KindResponse))
	require.Equal(t, 400, DefaultBlockStatus(pluginapi.KindPrompt))
}

func TestRunReadersEditRequestHeadersConcurrently(t *testing.T) {
	reader := func(name string, edit func(http.Header)) *Instance {
		return newTestInstance(&fakePlugin{name: name, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
			edit(x.Headers.Request)
			time.Sleep(10 * time.Millisecond)
			return pluginapi.Allow(), nil
		}}, InstanceSpec{})
	}
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{
		{reader("set", func(h http.Header) { h.Set("X-Team", "platform") }), 10},
		{reader("remove", func(h http.Header) { h.Del("X-Debug") }), 10},
		{reader("keep", func(h http.Header) { h.Set("X-Keep", h.Get("X-Keep")) }), 10},
	})
	require.NoError(t, err)

	x := withPromptText(newExchange(), "hi")
	x.Headers.Request = http.Header{"X-Debug": {"1"}, "X-Keep": {"same"}}
	_, err = chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Headers.Request.Get("X-Team")
	require.Equal(t, "platform", got)
	_, ok := x.Headers.Request["X-Debug"]
	require.False(t, ok)
	got = x.Headers.Request.Get("X-Keep")
	require.Equal(t, "same", got)
}

// A mutator that outlives its timeout keeps writing the request's exchange.
// The run reports the failure as abandoned so callers know not to read it.
func TestRunAbandonedMutatorIsReportedAbandoned(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	mutator := &fakePlugin{name: "runaway", mutates: true, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		for {
			select {
			case <-stop:
				return pluginapi.Allow(), nil
			default:
				_ = x.Prompt.SetText("m0", 0, "still editing")
			}
		}
	}}
	inst := newTestInstance(mutator, InstanceSpec{Timeout: 20 * time.Millisecond, FailMode: FailOpen})
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, 10}})
	require.NoError(t, err)

	_, err = chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
	require.True(t, Abandoned(err))
}

// A plugin may hand back any status; only 4xx and 5xx can be written.
func TestBlockErrorClampsStatus(t *testing.T) {
	for _, status := range []int{42, 200, 399, 600, 1000, -1} {
		got := BlockError(pluginapi.Block(status, "x", "y"), 502)
		assert.Equal(t, 502, got.HTTPStatusCode(), "status %d rendered as %d, want the phase default 502", status, got.HTTPStatusCode())
	}
	got := BlockError(pluginapi.Block(451, "x", "y"), 502)
	assert.Equal(t, 451, got.HTTPStatusCode())
	got = BlockError(pluginapi.Block(0, "x", "y"), 99)
	assert.Equal(t, 400, got.HTTPStatusCode())
}
