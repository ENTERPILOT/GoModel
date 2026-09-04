package plugins

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
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
	if err != nil {
		t.Fatal(err)
	}
	x := withPromptText(newExchange(), "hello")
	outcome, err := chain.RunPrompt(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "editor" || order[1] != "checker" {
		t.Fatalf("order = %v", order)
	}
	if outcome.Decision.Action != pluginapi.ActionWarn || outcome.Instance != "checker" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(outcome.Records) != 2 || outcome.Records[0].Instance != "editor" {
		t.Fatalf("records = %+v", outcome.Records)
	}
	if v, _ := x.Values.Get("editor.ran"); v != true {
		t.Fatal("values not shared")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	x := withPromptText(newExchange(), "hi")
	outcome, err := chain.RunPrompt(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&maxInFlight) < 2 {
		t.Fatalf("readers did not run concurrently (max in flight %d)", maxInFlight)
	}
	if outcome.Decision.Action != pluginapi.ActionBlock || outcome.Decision.Status != 451 || outcome.Instance != "block" {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, name := range []string{"warn", "respond", "block"} {
		if _, ok := x.Values.Get(name); !ok {
			t.Fatalf("value %s not merged back", name)
		}
		if x.Headers.Response.Get("X-"+name) != "1" {
			t.Fatalf("header X-%s not merged back", name)
		}
	}
	if len(outcome.Records) != 3 {
		t.Fatalf("records = %d", len(outcome.Records))
	}
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
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
			if tt.wantErr {
				var pluginErr *PluginError
				if !errors.As(err, &pluginErr) || pluginErr.Instance != tt.plugin.name {
					t.Fatalf("error = %v, want PluginError for %s", err, tt.plugin.name)
				}
				if len(outcome.Records) != 1 || outcome.Records[0].Err == nil {
					t.Fatalf("records = %+v", outcome.Records)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil (fail open)", err)
			}
			if outcome.Decision.Action != pluginapi.ActionAllow || len(outcome.Records) != 2 || outcome.Records[0].Err == nil {
				t.Fatalf("outcome = %+v", outcome)
			}
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
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := chain.RunResponse(context.Background(), newExchange())
	if err != nil || outcome.Decision.Action != pluginapi.ActionRespond || outcome.Decision.Response == nil {
		t.Fatalf("RunResponse = %+v, %v (respond without completion must be normalized)", outcome, err)
	}
	stream, err := BuildChain(pluginapi.KindStream, []Ref{{inst, 1}})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = stream.RunStreamEnd(context.Background(), newExchange())
	if err != nil || outcome.Decision.Action != pluginapi.ActionBlock {
		t.Fatalf("RunStreamEnd = %+v, %v", outcome, err)
	}
	var empty *Chain
	if outcome, err := empty.RunPrompt(context.Background(), newExchange()); err != nil || outcome.Decision.Action != pluginapi.ActionAllow {
		t.Fatalf("empty chain = %+v, %v", outcome, err)
	}
}

func TestDecisionHelpers(t *testing.T) {
	if MergeDecision(pluginapi.Warn("a", "", nil), pluginapi.Allow()).Action != pluginapi.ActionWarn {
		t.Fatal("merge lowered severity")
	}
	blocked := BlockError(pluginapi.Block(0, "", ""), 400)
	if blocked.HTTPStatusCode() != 400 || blocked.Code == nil || *blocked.Code != CodeBlocked || blocked.Message == "" {
		t.Fatalf("BlockError defaults = %+v", blocked)
	}
	custom := BlockError(pluginapi.Block(502, "x", "y"), 400)
	if custom.HTTPStatusCode() != 502 || *custom.Code != "x" || custom.Message != "y" {
		t.Fatalf("BlockError custom = %+v", custom)
	}
	failure := FailureError(errFake)
	if failure.HTTPStatusCode() != 500 || *failure.Code != CodePluginFailure || failure.Message == errFake.Error() {
		t.Fatalf("FailureError = %+v", failure)
	}
	if WarnHeaderValue(pluginapi.Warn("pii", "", nil)) != "warn; code=pii" {
		t.Fatal("WarnHeaderValue wrong")
	}
	if DefaultBlockStatus(pluginapi.KindResponse) != 502 || DefaultBlockStatus(pluginapi.KindPrompt) != 400 {
		t.Fatal("DefaultBlockStatus wrong")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	x := withPromptText(newExchange(), "hi")
	x.Headers.Request = http.Header{"X-Debug": {"1"}, "X-Keep": {"same"}}
	if _, err := chain.RunPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Headers.Request.Get("X-Team"); got != "platform" {
		t.Fatalf("X-Team = %q, want platform", got)
	}
	if _, ok := x.Headers.Request["X-Debug"]; ok {
		t.Fatal("X-Debug removed by a reader is still present")
	}
	if got := x.Headers.Request.Get("X-Keep"); got != "same" {
		t.Fatalf("X-Keep = %q, want same", got)
	}
}
