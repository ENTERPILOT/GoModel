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
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := chain.RunPrompt(context.Background(), withPromptText(newExchange(), "hello"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"editor": true, "noop": false, "reader": false, "again": true}
	if len(outcome.Records) != len(want) {
		t.Fatalf("records = %+v", outcome.Records)
	}
	seen := make(map[string]bool, len(want))
	for _, record := range outcome.Records {
		expected, ok := want[record.Instance]
		if !ok {
			t.Errorf("unexpected record %q", record.Instance)
			continue
		}
		seen[record.Instance] = true
		if record.Edited != expected {
			t.Errorf("%s: edited = %v, want %v", record.Instance, record.Edited, expected)
		}
	}
	for instance := range want {
		if !seen[instance] {
			t.Errorf("missing record %q", instance)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	observe := func(instance string, x *pluginapi.Exchange) {
		seen = append(seen, instance+": "+x.Prompt.Messages[0].Text())
	}
	if _, err := chain.RunPromptObserved(context.Background(), withPromptText(newExchange(), "hello"), observe); err != nil {
		t.Fatal(err)
	}
	want := []string{"first: hello one", "second: hello one two"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("observed = %q, want %q", seen, want)
	}

	// A mutator that edits and then fails closed is observed too, before
	// the run returns its error.
	failing := newTestInstance(&fakePlugin{name: "failing", mutates: true, onPrompt: func(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		_ = x.Prompt.SetText("m0", 0, "edited then failed")
		return pluginapi.Allow(), errors.New("boom")
	}}, InstanceSpec{FailMode: FailClosed})
	chain, err = BuildChain(pluginapi.KindPrompt, []Ref{{failing, 10}, {second, 20}})
	if err != nil {
		t.Fatal(err)
	}
	seen = nil
	if _, err := chain.RunPromptObserved(context.Background(), withPromptText(newExchange(), "hello"), observe); err == nil {
		t.Fatal("expected the fail-closed error")
	}
	if len(seen) != 1 || seen[0] != "failing: edited then failed" {
		t.Fatalf("observed = %q, want the failing mutator's edit only", seen)
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
	if err != nil {
		t.Fatal(err)
	}
	_, err = chain.RunPrompt(context.Background(), withPromptText(newExchange(), "x"))
	if !Abandoned(err) {
		t.Fatalf("error = %v, want an abandoned failure even under fail_open", err)
	}
}

// A plugin may hand back any status; only 4xx and 5xx can be written.
func TestBlockErrorClampsStatus(t *testing.T) {
	for _, status := range []int{42, 200, 399, 600, 1000, -1} {
		got := BlockError(pluginapi.Block(status, "x", "y"), 502)
		if got.HTTPStatusCode() != 502 {
			t.Errorf("status %d rendered as %d, want the phase default 502", status, got.HTTPStatusCode())
		}
	}
	if got := BlockError(pluginapi.Block(451, "x", "y"), 502); got.HTTPStatusCode() != 451 {
		t.Errorf("status 451 rendered as %d", got.HTTPStatusCode())
	}
	if got := BlockError(pluginapi.Block(0, "x", "y"), 99); got.HTTPStatusCode() != 400 {
		t.Errorf("unusable default rendered as %d, want 400", got.HTTPStatusCode())
	}
}
