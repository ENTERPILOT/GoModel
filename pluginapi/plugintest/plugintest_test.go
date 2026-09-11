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
	// Window 2 shows the withheld tail "y se" in front of "cret is".
	if len(r.windows) != 3 || r.windows[1] != "y secret is" {
		t.Errorf("windows = %q", r.windows)
	}
	if res.End.Message != "my [x] is safe" {
		t.Errorf("stream state at end = %q", res.End.Message)
	}
	if res.Terminated != nil || len(res.Events) == 0 || res.Events[len(res.Events)-2].Kind != pluginapi.EventFinish {
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
