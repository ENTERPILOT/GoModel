package plugintest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	assert.Equal(t, "my [x] is safe", res.Text[0])

	// Window 2 shows the withheld tail "y se" in front of "cret is"; the
	// finish event flushes the last tail, shown once more on its own.
	require.Len(t, r.windows, 4)
	assert.Equal(t, "y secret is", r.windows[1])
	assert.Equal(t, "safe", r.windows[3])
	assert.Equal(t, "my [x] is safe", res.End.Message)

	// The finish event flushed the tail before it was delivered itself.
	assert.Nil(t, res.Terminated)
	require.Len(t, res.Events, 5)
	assert.Equal(t, "safe", res.Events[3].Text)
	assert.Equal(t, pluginapi.EventFinish, res.Events[4].Kind)
}

func TestRunStreamCoalescesAndTerminates(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, MinChunkChars: 6}}
	res, err := RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{
		TextDelta("ab"), TextDelta("cd"), TextDelta("ef"), TextDelta("g"), TextDelta("stop!"), TextDelta("never"),
	})
	require.NoError(t, err)
	require.Len(t, r.windows, 2)
	assert.Equal(t, "abcdef", r.windows[0])
	assert.Equal(t, "gstop!", r.windows[1])
	require.NotNil(t, res.Terminated)
	assert.Equal(t, 451, res.Terminated.Status)
	assert.Equal(t, "abcdef", res.Text[0], "result = %+v", res)
}

func TestRunStreamObserveAndBuffer(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamObserve}}
	res, err := RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("a secret")})
	require.NoError(t, err)
	assert.Equal(t, "a secret", res.Text[0], "observe: %+v, %v", res, err)

	r = &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}}
	res, err = RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("a se"), TextDelta("cret")})
	require.NoError(t, err)
	assert.Equal(t, "a [x]", res.Text[0])
	assert.Empty(t, r.windows)
	assert.NotNil(t, res.Response, "buffer: %+v, %v", res, err)

	res, err = RunStream(context.Background(), r, nil, []*pluginapi.StreamEvent{TextDelta("stop")})
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, res.End.Action)
	assert.Empty(t, res.Text, "buffer block: %+v, %v", res, err)
}

func TestHostAndFixtures(t *testing.T) {
	h := NewHost("yes")
	h.Finish = "length"
	c, err := h.Complete(context.Background(), pluginapi.InferenceRequest{Model: "m"})
	require.NoError(t, err)
	assert.Equal(t, "yes", c.Text(0))
	assert.Equal(t, "length", c.Choices[0].FinishReason, "reply = %+v, %v", c, err)
	c, _ = h.Complete(context.Background(), pluginapi.InferenceRequest{})
	assert.Empty(t, c.Choices)
	require.Len(t, h.Requests(), 2)
	assert.Equal(t, "m", h.Requests()[0].Model)

	h.Err = errors.New("down")
	_, err = h.Complete(context.Background(), pluginapi.InferenceRequest{})
	assert.Error(t, err)

	h.Metrics().Inc("calls", map[string]string{"k": "v"})
	h.Metrics().Inc("calls", nil)
	h.Metrics().Observe("latency", 1.5, nil)
	m := h.Recorded()
	assert.Equal(t, 2, m.Counts["calls"])
	assert.Len(t, m.Values["latency"], 1)
	assert.Nil(t, m.Labels["calls"], "metrics = %+v", m)
	assert.NotNil(t, h.HTTPClient())
	assert.NotNil(t, h.Logger())

	x := Exchange(Prompt(Text(pluginapi.RoleUser, "m1", "hi")), Completion("a", "b"))
	assert.Equal(t, "hi", x.Prompt.Message("m1").Text())
	assert.Equal(t, "b", x.Response.Text(1))
	assert.NotNil(t, x.Values)
	assert.NotNil(t, x.Stream)
	assert.NotEmpty(t, x.Meta.RequestID, "exchange = %+v", x)
	assert.False(t, x.Prompt.Changes().Dirty)

	p := Init(t, func() pluginapi.Plugin { return &redactor{} }, "", nil)
	assert.NotNil(t, p)
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
	require.NoError(t, err)

	var kinds []string
	for _, ev := range res.Events {
		kinds = append(kinds, string(ev.Kind)+":"+ev.Text)
	}
	// Both pending choices flush, in order of first appearance, before the
	// reasoning delta; the usage event was dropped; the tail comes last.
	want := []string{"text_delta:one", "text_delta:zero", "reasoning_delta:think", "text_delta: more"}
	assert.Equal(t, strings.Join(want, ","), strings.Join(kinds, ","), "events = %v, want %v", kinds, want)
	assert.Equal(t, "zero more", res.Text[0])
	assert.Equal(t, "one", res.Text[1])
	assert.Len(t, res.Text, 2)

	// A dropped event never reached the stream state; the reasoning one did.
	assert.Equal(t, "zero more", l.end.Message)
	assert.NotEqual(t, 0, res.Events[2].Seq, "end = %+v", l.end)
}

func TestRunStreamBufferedRespondAndPresetResponse(t *testing.T) {
	r := &redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}}
	x := Exchange(nil, nil)
	res, err := RunStream(context.Background(), r, x, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventReasoningDelta, Text: "hmm"}, TextDelta("a secret"),
	})
	assert.NoError(t, err)
	assert.Equal(t, "a [x]", res.Text[0])
	require.Len(t, res.Response.Choices[0].Message.Parts, 2)
	assert.Equal(t, pluginapi.PartReasoning, res.Response.Choices[0].Message.Parts[0].Kind, "assembled = %+v, %v", res, err)

	// A preset response is handed to the hook as is.
	preset := Completion("keep")
	preset.Choices[0].FinishReason = "length"
	x = Exchange(nil, preset)
	res, err = RunStream(context.Background(), r, x, []*pluginapi.StreamEvent{TextDelta("ignored")})
	require.NoError(t, err)
	assert.Equal(t, "keep", res.Text[0])
	assert.Equal(t, "length", res.Response.Choices[0].FinishReason, "preset = %+v, %v", res, err)

	// A respond decision is what the client receives.
	answer := &responder{}
	res, err = RunStream(context.Background(), answer, Exchange(nil, nil), []*pluginapi.StreamEvent{TextDelta("anything")})
	require.NoError(t, err)
	assert.Equal(t, "no", res.Text[0])
	assert.Equal(t, pluginapi.ActionRespond, res.End.Action, "respond = %+v, %v", res, err)
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
	require.NoError(t, err)
	assert.Equal(t, "n=1", c.Text(0))
	assert.Equal(t, 1, h.Recorded().Counts["seen"], "reply = %+v, %v", c, err)
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
	require.NoError(t, err)

	// The first tool-call delta flushed the text window, so its
	// unfinished "se" was delivered as is; call 0's split match was
	// rewritten; call 1 has its own window.
	assert.Equal(t, "text se", res.Text[0])
	assert.Equal(t, `{"a":"[x]"}`, res.ToolArguments[0][0])
	assert.Equal(t, `{"b":1}`, res.ToolArguments[0][1], "result = %+v", res)

	// Every window is shown on arrival and once more, in full, when it is
	// flushed: the text by call 0's first delta (another kind), both calls
	// by the end of the stream. Parallel calls do not flush each other.
	want := []string{"text se", "t se", `{"a":"se`, `:"secret"}`, `{"b":1}`, `x]"}`, `":1}`}
	assert.Equal(t, strings.Join(want, "|"), strings.Join(a.windows, "|"), "windows = %q, want %q", a.windows, want)
}

func TestRunStreamReopenedWindowQueuesBehindPending(t *testing.T) {
	a := &argsRedactor{redactor{policy: pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, MinChunkChars: 100}}}
	res, err := RunStream(context.Background(), a, nil, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: "zero-a"},
		{Kind: pluginapi.EventTextDelta, Choice: 1, Text: "one"},
		{Kind: pluginapi.EventToolCallDelta, Choice: 0, Call: 0, Text: "{}"}, // flushes choice 0's text
		{Kind: pluginapi.EventTextDelta, Choice: 0, Text: "zero-b"},          // reopens it, behind choice 1
	})
	require.NoError(t, err)

	var order []string
	for _, ev := range res.Events {
		order = append(order, string(ev.Kind)+":"+ev.Text)
	}
	// The tool-call delta flushed choice 0's text; reopening that text
	// flushed the tool call and queued the text behind choice 1's.
	want := "text_delta:zero-a,tool_call_delta:{},text_delta:one,text_delta:zero-b"
	assert.Equal(t, want, strings.Join(order, ","))
}
