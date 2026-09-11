package plugintest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

// redactor is a transform-mode hook that rewrites "secret" to "[x]" in each
// window, skipping matches that end inside the overlap, and terminates on
// "stop". It counts the windows it saw.
type redactor struct {
	policy  pluginapi.StreamPolicy
	windows []string
	end     pluginapi.Decision
}

func (r *redactor) Manifest() pluginapi.Manifest { return pluginapi.Manifest{Name: "redactor"} }
func (r *redactor) Init(context.Context, json.RawMessage, pluginapi.Host) error {
	return nil
}
func (r *redactor) Close(context.Context) error          { return nil }
func (r *redactor) StreamPolicy() pluginapi.StreamPolicy { return r.policy }
func (r *redactor) OnStreamEvent(_ context.Context, _ *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if ev.Kind != pluginapi.EventTextDelta {
		return pluginapi.Pass(), nil
	}
	r.windows = append(r.windows, ev.Text)
	if strings.Contains(ev.Text, "stop") {
		return pluginapi.Terminate(pluginapi.Block(451, "stopped", "cut")), nil
	}
	skip := 0
	for i := 0; i < ev.Overlap && skip < len(ev.Text); i++ {
		skip++
	}
	// Matches ending inside the overlap were handled before.
	out, changed := ev.Text, false
	for idx := strings.Index(out, "secret"); idx >= 0; idx = strings.Index(out, "secret") {
		if idx+len("secret") <= skip {
			break
		}
		out = out[:idx] + "[x]" + out[idx+len("secret"):]
		changed = true
	}
	if !changed {
		return pluginapi.Pass(), nil
	}
	return pluginapi.Replace(out), nil
}
func (r *redactor) OnStreamEnd(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	r.end = pluginapi.Warn("seen", x.Stream.Text(0), nil)
	return r.end, nil
}
func (r *redactor) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if strings.Contains(x.Response.Text(0), "stop") {
		return pluginapi.Block(0, "stopped", "cut"), nil
	}
	return pluginapi.Allow(), x.Response.ReplaceText(0, strings.ReplaceAll(x.Response.Text(0), "secret", "[x]"))
}

func TestRunStreamTransformLookbehind(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 4}}
	res, err := RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{
		TextDelta("my se"), TextDelta("cret is"), TextDelta(" safe"), Event(pluginapi.EventFinish),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text[0] != "my [x] is safe" {
		t.Errorf("text = %q", res.Text[0])
	}
	// Window 2 shows the withheld tail "y se" in front of "cret is"; the
	// finish event flushes the last tail, shown once more on its own.
	if len(r.windows) != 4 || r.windows[1] != "y secret is" || r.windows[3] != "safe" {
		t.Errorf("windows = %q", r.windows)
	}
	if res.End.Message != "my [x] is safe" {
		t.Errorf("stream state at end = %q", res.End.Message)
	}
	// The finish event flushed the tail before it was delivered itself.
	if res.Terminated != nil || len(res.Events) != 5 || res.Events[3].Text != "safe" || res.Events[4].Kind != pluginapi.EventFinish {
		t.Errorf("events = %+v", res.Events)
	}
}

func TestRunStreamCoalescesAndTerminates(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, MinChunkChars: 6}}
	res, err := RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{
		TextDelta("ab"), TextDelta("cd"), TextDelta("ef"), TextDelta("g"), TextDelta("stop!"), TextDelta("never"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.windows) != 2 || r.windows[0] != "abcdef" || r.windows[1] != "gstop!" {
		t.Errorf("windows = %q", r.windows)
	}
	if res.Terminated == nil || res.Terminated.Status != 451 || res.Text[0] != "abcdef" {
		t.Errorf("result = %+v", res)
	}
}

func TestRunStreamObserveAndBuffer(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamObserve}}
	res, err := RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("a secret")})
	if err != nil || res.Text[0] != "a secret" {
		t.Errorf("observe: %+v, %v", res, err)
	}
	r = &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}}
	res, err = RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("a se"), TextDelta("cret")})
	if err != nil || res.Text[0] != "a [x]" || len(r.windows) != 0 || res.Response == nil {
		t.Errorf("buffer: %+v, %v", res, err)
	}
	res, err = RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("stop")})
	if err != nil || res.End.Action != pluginapi.ActionBlock || len(res.Text) != 0 {
		t.Errorf("buffer block: %+v, %v", res, err)
	}
}

func TestHostAndFixtures(t *testing.T) {
	h := NewHost("yes")
	h.Finish = "length"
	c, err := h.Complete(context.Background(), pluginapi.InferenceRequest{Model: "m"})
	if err != nil || c.Text(0) != "yes" || c.Choices[0].FinishReason != "length" {
		t.Errorf("reply = %+v, %v", c, err)
	}
	if c, _ := h.Complete(context.Background(), pluginapi.InferenceRequest{}); len(c.Choices) != 0 {
		t.Error("replies not exhausted")
	}
	if len(h.Requests()) != 2 || h.Requests()[0].Model != "m" {
		t.Errorf("requests = %+v", h.Requests())
	}
	h.Err = errors.New("down")
	if _, err := h.Complete(context.Background(), pluginapi.InferenceRequest{}); err == nil {
		t.Error("Err ignored")
	}
	h.Metrics().Inc("calls", map[string]string{"k": "v"})
	h.Metrics().Inc("calls", nil)
	h.Metrics().Observe("latency", 1.5, nil)
	m := h.Recorded()
	if m.Counts["calls"] != 2 || len(m.Values["latency"]) != 1 || m.Labels["calls"] != nil {
		t.Errorf("metrics = %+v", m)
	}
	if h.HTTPClient() == nil || h.Logger() == nil {
		t.Error("nil client or logger")
	}
	x := Exchange(Prompt(Text(pluginapi.RoleUser, "m1", "hi")), Completion("a", "b"))
	if x.Prompt.Message("m1").Text() != "hi" || x.Response.Text(1) != "b" || x.Values == nil || x.Stream == nil || x.Meta.RequestID == "" {
		t.Errorf("exchange = %+v", x)
	}
	if x.Prompt.Changes().Dirty {
		t.Error("prompt starts dirty")
	}
	p := Init(t, func() pluginapi.Plugin { return &redactor{} }, "", nil)
	if p == nil {
		t.Error("Init")
	}
}

// lower is a transform hook that lower-cases reasoning deltas and drops
// usage events.
type lower struct{ redactor }

func (l *lower) OnStreamEvent(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	switch ev.Kind {
	case pluginapi.EventReasoningDelta:
		if ev.Overlap != 0 {
			return pluginapi.Replace("overlap leaked"), nil
		}
		return pluginapi.Replace(strings.ToLower(ev.Text)), nil
	case pluginapi.EventUsage:
		return pluginapi.Drop(), nil
	}
	return l.redactor.OnStreamEvent(ctx, x, ev)
}

func TestRunStreamMultiChoiceAndOtherEvents(t *testing.T) {
	l := &lower{redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, MinChunkChars: 100}}}
	res, err := RunStream(context.Background(), l, nil, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventTextDelta, Choice: 1, Text: "one"},
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: "zero"},
		{Kind: pluginapi.EventReasoningDelta, Text: "THINK", Overlap: 3},
		{Kind: pluginapi.EventUsage},
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: " more"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range res.Events {
		kinds = append(kinds, string(ev.Kind)+":"+ev.Text)
	}
	// Both pending choices flush, in order of first appearance, before the
	// reasoning delta; the usage event was dropped; the tail comes last.
	want := []string{"text_delta:one", "text_delta:zero", "reasoning_delta:think", "text_delta: more"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", kinds, want)
	}
	if res.Text[0] != "zero more" || res.Text[1] != "one" || len(res.Text) != 2 {
		t.Errorf("text = %v", res.Text)
	}
	// A dropped event never reached the stream state; the reasoning one did.
	if l.end.Message != "zero more" || res.Events[2].Seq == 0 {
		t.Errorf("end = %+v", l.end)
	}
}

func TestRunStreamBufferedRespondAndPresetResponse(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}}
	x := Exchange(nil, nil)
	res, err := RunStream(context.Background(), r, x, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventReasoningDelta, Text: "hmm"}, TextDelta("a secret"),
	})
	if err != nil || res.Text[0] != "a [x]" || len(res.Response.Choices[0].Message.Parts) != 2 || res.Response.Choices[0].Message.Parts[0].Kind != pluginapi.PartReasoning {
		t.Errorf("assembled = %+v, %v", res, err)
	}
	// A preset response is handed to the hook as is.
	preset := Completion("keep")
	preset.Choices[0].FinishReason = "length"
	x = Exchange(nil, preset)
	res, err = RunStream(context.Background(), r, x, []*pluginapi.StreamEvent{TextDelta("ignored")})
	if err != nil || res.Text[0] != "keep" || res.Response.Choices[0].FinishReason != "length" {
		t.Errorf("preset = %+v, %v", res, err)
	}
	// A respond decision is what the client receives.
	answer := &responder{}
	res, err = RunStream(context.Background(), answer, Exchange(nil, nil), []*pluginapi.StreamEvent{TextDelta("anything")})
	if err != nil || res.Text[0] != "no" || res.End.Action != pluginapi.ActionRespond {
		t.Errorf("respond = %+v, %v", res, err)
	}
}

type responder struct{ redactor }

func (r *responder) StreamPolicy() pluginapi.StreamPolicy {
	return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
}
func (r *responder) OnResponse(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Respond("no"), nil
}

func TestHostReplyMayInspectHost(t *testing.T) {
	h := NewHost()
	h.Reply = func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
		h.Metrics().Inc("seen", nil)
		return &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "n="+string(rune('0'+len(h.Requests()))))}}}, nil
	}
	c, err := h.Complete(context.Background(), pluginapi.InferenceRequest{})
	if err != nil || c.Text(0) != "n=1" || h.Recorded().Counts["seen"] != 1 {
		t.Errorf("reply = %+v, %v", c, err)
	}
}

// argsRedactor replaces "secret" in text and tool-call windows.
type argsRedactor struct{ redactor }

func (a *argsRedactor) OnStreamEvent(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if ev.Kind == pluginapi.EventToolCallDelta {
		copy := *ev
		copy.Kind = pluginapi.EventTextDelta
		return a.redactor.OnStreamEvent(ctx, x, &copy)
	}
	return a.redactor.OnStreamEvent(ctx, x, ev)
}

func TestRunStreamToolCallWindows(t *testing.T) {
	a := &argsRedactor{redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 4}}}
	res, err := RunStream(context.Background(), a, nil, []*pluginapi.StreamEvent{
		TextDelta("text se"),
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `{"a":"se`},
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `cret"}`},
		{Kind: pluginapi.EventToolCallDelta, Call: 1, Text: `{"b":1}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first tool-call delta flushed the text window, so its
	// unfinished "se" was delivered as is; call 0's split match was
	// rewritten; call 1 has its own window.
	if res.Text[0] != "text se" || res.ToolArguments[0][0] != `{"a":"[x]"}` || res.ToolArguments[0][1] != `{"b":1}` {
		t.Errorf("result = %+v", res)
	}
	// Every window is shown on arrival and once more, in full, when it is
	// flushed: the text by call 0's first delta (another kind), both calls
	// by the end of the stream. Parallel calls do not flush each other.
	want := []string{"text se", "t se", `{"a":"se`, `:"secret"}`, `{"b":1}`, `x]"}`, `":1}`}
	if strings.Join(a.windows, "|") != strings.Join(want, "|") {
		t.Errorf("windows = %q, want %q", a.windows, want)
	}
}

func TestRunStreamReopenedWindowQueuesBehindPending(t *testing.T) {
	a := &argsRedactor{redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, MinChunkChars: 100}}}
	res, err := RunStream(context.Background(), a, nil, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: "zero-a"},
		{Kind: pluginapi.EventTextDelta, Choice: 1, Text: "one"},
		{Kind: pluginapi.EventToolCallDelta, Choice: 0, Call: 0, Text: "{}"}, // flushes choice 0's text
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: "zero-b"},          // reopens it, behind choice 1
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, ev := range res.Events {
		order = append(order, string(ev.Kind)+":"+ev.Text)
	}
	// The tool-call delta flushed choice 0's text; reopening that text
	// flushed the tool call and queued the text behind choice 1's.
	want := "text_delta:zero-a,tool_call_delta:{},text_delta:one,text_delta:zero-b"
	if strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
}
