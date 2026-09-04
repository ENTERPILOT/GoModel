package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/pluginapi"
)

// streamDialect binds the pieces of one canonical stream dialect a plugin
// stream wrapper needs: the codec, and how to assemble, map, re-apply and
// re-synthesize the response for buffered runs.
type streamDialect struct {
	codec  func() streaming.Codec
	finish func(events []streaming.Event, run func(*pluginapi.Completion) (plugins.Outcome, error)) ([]byte, error)
}

func chatStreamDialect(includeUsage bool) streamDialect {
	return streamDialect{
		codec: streaming.ChatCodec,
		finish: func(events []streaming.Event, run func(*pluginapi.Completion) (plugins.Outcome, error)) ([]byte, error) {
			resp, err := streaming.AssembleChatResponse(events)
			if errors.Is(err, streaming.ErrNoEvents) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			completion, err := exchange.FromChatResponse(resp)
			if err != nil {
				return nil, err
			}
			outcome, err := run(completion)
			if err != nil {
				return nil, err
			}
			// The provider usage chunk is relayed to every client on a pass
			// through, and the usage observer reads it downstream of this
			// wrapper, so a replay keeps it whenever the upstream sent one.
			var usage any
			if hasChatUsage(resp.Usage) {
				includeUsage = true
				usage = &resp.Usage
			}
			switch outcome.Decision.Action {
			case pluginapi.ActionBlock:
				return nil, blockedStream(outcome.Decision, usage)
			case pluginapi.ActionRespond:
				synthesized := exchange.CompletionToChatResponse(outcome.Decision.Response, resp.Model)
				synthesized.Usage = resp.Usage
				return streaming.SynthesizeChatStream(synthesized, includeUsage), nil
			}
			if !completion.Changes().Dirty {
				return nil, nil
			}
			applied, err := exchange.ApplyToChatResponse(resp, completion)
			if err != nil {
				return nil, err
			}
			return streaming.SynthesizeChatStream(applied, includeUsage), nil
		},
	}
}

func responsesStreamDialect() streamDialect {
	return streamDialect{
		codec: streaming.ResponsesCodec,
		finish: func(events []streaming.Event, run func(*pluginapi.Completion) (plugins.Outcome, error)) ([]byte, error) {
			resp, err := streaming.AssembleResponsesResponse(events)
			if errors.Is(err, streaming.ErrNoEvents) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			completion, err := exchange.FromResponsesResponse(resp)
			if err != nil {
				return nil, err
			}
			outcome, err := run(completion)
			if err != nil {
				return nil, err
			}
			var usage any
			if resp.Usage != nil {
				usage = resp.Usage
			}
			switch outcome.Decision.Action {
			case pluginapi.ActionBlock:
				return nil, blockedStream(outcome.Decision, usage)
			case pluginapi.ActionRespond:
				synthesized := exchange.CompletionToResponsesResponse(outcome.Decision.Response, resp.Model)
				synthesized.Usage = resp.Usage
				return streaming.SynthesizeResponsesStream(synthesized), nil
			}
			if !completion.Changes().Dirty {
				return nil, nil
			}
			applied, err := exchange.ApplyToResponsesResponse(resp, completion)
			if err != nil {
				return nil, err
			}
			return streaming.SynthesizeResponsesStream(applied), nil
		},
	}
}

func hasChatUsage(u core.Usage) bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// streamBlocked carries a block decision (and the usage of the discarded
// response) out of a buffered finisher so the wrapper can render it with the
// codec's terminal events.
type streamBlocked struct {
	decision pluginapi.Decision
	usage    any
}

func (e *streamBlocked) Error() string { return "stream blocked by plugin" }

func blockedStream(d pluginapi.Decision, usage any) error {
	return &streamBlocked{decision: d, usage: usage}
}

// inFlightInstance is a stream instance driven event by event; observe says
// its replace and drop decisions are ignored.
type inFlightInstance struct {
	inst    *plugins.Instance
	observe bool
}

// wrapPluginStream wraps a provider stream with the request's stream and
// response phase plugins. It returns stream unchanged when there are none;
// prompt is only called when a wrapper is built. Buffering is used when the
// response chain is non-empty or any stream instance asks for it; otherwise
// events are transformed in flight.
func (s *translatedInferenceService) wrapPluginStream(ctx context.Context, workflow *core.Workflow, dialect streamDialect, prompt func() *pluginapi.Prompt, stream io.ReadCloser) io.ReadCloser {
	chains := s.pluginChainsFor(ctx)
	if chains == nil || (chains.Stream.Empty() && chains.Response.Empty()) {
		return stream
	}
	state := plugins.RequestStateFor(ctx)
	x := state.NewExchange(ctx, pluginMeta(ctx, workflow))
	if prompt != nil {
		x.Prompt = prompt()
	}
	x.Stream = &pluginapi.StreamState{}
	ps := &pluginStream{ctx: ctx, chains: chains, state: state, x: x, requestID: x.Meta.RequestID}

	var buffered []*plugins.Instance
	maxBuffer := 0
	for _, inst := range chains.Stream.Instances() {
		policy := inst.StreamPolicy()
		if policy.Mode == pluginapi.StreamBuffer {
			buffered = append(buffered, inst)
			maxBuffer = max(maxBuffer, policy.MaxBufferBytes)
			continue
		}
		ps.inFlight = append(ps.inFlight, inFlightInstance{inst: inst, observe: policy.Mode != pluginapi.StreamTransform})
		ps.lookbehind = max(ps.lookbehind, policy.LookbehindChars)
	}

	if !chains.Response.Empty() || len(buffered) > 0 {
		codec := dialect.codec()
		finisher := func(events []streaming.Event, _ []byte) ([]byte, error) {
			replay, err := dialect.finish(events, func(completion *pluginapi.Completion) (plugins.Outcome, error) {
				return ps.runResponse(completion, buffered)
			})
			if blocked, ok := errors.AsType[*streamBlocked](err); ok {
				termination := terminationFor(blocked.decision)
				termination.Usage = blocked.usage
				return concatChunks(codec.Terminate(*termination)), nil
			}
			return replay, err
		}
		stream = streaming.NewBufferedSSEStream(ctx, stream, codec, finisher, streaming.BufferOptions{
			MaxBytes: maxBuffer,
			OnError:  ps.reportError("buffer"),
		})
	}
	if len(ps.inFlight) > 0 {
		stream = streaming.NewTransformedSSEStream(stream, dialect.codec(), ps, streaming.TransformOptions{
			LookbehindChars: ps.lookbehind,
			OnError:         ps.reportError("transform"),
		})
	}
	return stream
}

// pluginStream drives the stream-phase instances of one request. It is the
// streaming.Transformer for transform and observe instances.
type pluginStream struct {
	ctx        context.Context
	chains     *plugins.Chains
	state      *plugins.RequestState
	x          *pluginapi.Exchange
	requestID  string
	inFlight   []inFlightInstance
	lookbehind int
}

func (ps *pluginStream) reportError(stage string) func(error) {
	return func(err error) {
		slog.Warn("plugin stream error", "request_id", ps.requestID, "stage", stage, "error", err)
	}
}

// runResponse runs the response chain plus the buffered stream instances'
// response hooks over the assembled completion.
func (ps *pluginStream) runResponse(completion *pluginapi.Completion, buffered []*plugins.Instance) (plugins.Outcome, error) {
	ps.x.Response = completion
	chain := ps.chains.Response
	if len(buffered) > 0 {
		var refs []plugins.Ref
		for _, step := range chain.StepsOf() {
			for _, inst := range step.Instances {
				refs = append(refs, plugins.Ref{Instance: inst, Step: step.Order})
			}
		}
		// Buffered instances run after the response chain, one per step in
		// their stream order: two of them may both mutate, which a shared
		// step would reject.
		next := 1
		if chain != nil && len(chain.Steps) > 0 {
			next = chain.Steps[len(chain.Steps)-1].Order + 1
		}
		for _, inst := range buffered {
			if inst.HasKind(pluginapi.KindResponse) && !chainHas(chain, inst) {
				refs = append(refs, plugins.Ref{Instance: inst, Step: next})
				next++
			}
		}
		merged, err := plugins.BuildChain(pluginapi.KindResponse, refs)
		if err != nil {
			return plugins.Outcome{}, err
		}
		chain = merged
	}
	outcome, err := chain.RunResponse(ps.ctx, ps.x)
	ps.state.Finish(ps.x)
	logResponseDecisions(ps.requestID, pluginapi.KindResponse, outcome, ps.state)
	ps.recordWarn(outcome)
	if err != nil {
		if pluginErr, ok := errors.AsType[*plugins.PluginError](err); ok {
			slog.Warn("response plugin failed closed", "request_id", ps.requestID, "instance", pluginErr.Instance, "error", pluginErr.Err)
		}
	}
	return outcome, err
}

func chainHas(chain *plugins.Chain, inst *plugins.Instance) bool {
	return slices.Contains(chain.Instances(), inst)
}

// OnEvent runs the in-flight stream instances over one event in step order.
// A replace feeds the next instance; drop and terminate end the walk.
func (ps *pluginStream) OnEvent(ev *streaming.Event) (streaming.Decision, error) {
	pev := &pluginapi.StreamEvent{Seq: ev.Seq + 1, Kind: pluginEventKind(ev.Kind), Choice: ev.Choice, Text: ev.Text, Raw: ev.Data}
	ps.appendState(pev, ev.Overlap)
	result := streaming.Decision{Action: streaming.ActionPass}
	for _, entry := range ps.inFlight {
		inst, observe := entry.inst, entry.observe
		hook, ok := inst.Plugin.(pluginapi.StreamHook)
		if !ok {
			continue
		}
		decision, err := plugins.Call(ps.ctx, inst, func(ctx context.Context) (pluginapi.StreamDecision, error) {
			return hook.OnStreamEvent(ctx, ps.x, pev)
		})
		if err != nil {
			if inst.FailsOpen(pluginapi.KindStream, err, true) {
				slog.Warn("stream plugin failed; continuing (fail_open)", "request_id", ps.requestID, "instance", inst.Name, "error", err)
				continue
			}
			ps.state.Record(plugins.DecisionRecord{Phase: pluginapi.KindStream, Instance: inst.Name, Err: err})
			return streaming.Decision{}, &plugins.PluginError{Instance: inst.Name, Phase: pluginapi.KindStream, Err: err}
		}
		switch decision.Action {
		case pluginapi.StreamTerminate:
			var term pluginapi.Decision
			if decision.Terminate != nil {
				term = *decision.Terminate
			}
			ps.state.Record(plugins.DecisionRecord{Phase: pluginapi.KindStream, Instance: inst.Name, Decision: term})
			return streaming.Decision{Action: streaming.ActionTerminate, Terminate: terminationFor(term)}, nil
		case pluginapi.StreamDrop:
			if observe {
				continue
			}
			return streaming.Decision{Action: streaming.ActionDrop}, nil
		case pluginapi.StreamReplace:
			if observe || pev.Kind != pluginapi.EventTextDelta && pev.Kind != pluginapi.EventReasoningDelta {
				continue
			}
			pev.Text = decision.Text
			result = streaming.Decision{Action: streaming.ActionReplace, Text: decision.Text}
		}
	}
	return result, nil
}

// OnEnd runs OnStreamEnd on the in-flight instances; block and respond
// decisions cut the stream.
func (ps *pluginStream) OnEnd() (*streaming.Termination, error) {
	if len(ps.inFlight) == 0 {
		return nil, nil
	}
	refs := make([]plugins.Ref, 0, len(ps.inFlight))
	for i, entry := range ps.inFlight {
		refs = append(refs, plugins.Ref{Instance: entry.inst, Step: i})
	}
	chain, err := plugins.BuildChain(pluginapi.KindStream, refs)
	if err != nil {
		return nil, err
	}
	outcome, err := chain.RunStreamEnd(ps.ctx, ps.x)
	logResponseDecisions(ps.requestID, pluginapi.KindStream, outcome, ps.state)
	ps.recordWarn(outcome)
	if err != nil {
		return nil, err
	}
	return terminationFor(outcome.Decision), nil
}

// recordWarn adds the warn header for a warn outcome. It reaches the client
// only when the headers are not committed yet: a buffered stream that
// finished before its first keep-alive. Later warns stay in the audit trail.
func (ps *pluginStream) recordWarn(outcome plugins.Outcome) {
	if outcome.Decision.Action == pluginapi.ActionWarn {
		ps.state.AddResponseHeader(plugins.GuardrailHeader, plugins.WarnHeaderValue(outcome.Decision))
	}
}

// appendState records the new text of an event in the exchange stream state.
// Under lookbehind the event repeats overlap runes already seen.
func (ps *pluginStream) appendState(ev *pluginapi.StreamEvent, overlap int) {
	if overlap <= 0 || ev.Kind != pluginapi.EventTextDelta {
		ps.x.Stream.Append(ev)
		return
	}
	offset := 0
	for i := 0; i < overlap && offset < len(ev.Text); i++ {
		_, size := utf8.DecodeRuneInString(ev.Text[offset:])
		offset += size
	}
	fresh := *ev
	fresh.Text = ev.Text[offset:]
	ps.x.Stream.Append(&fresh)
}

func pluginEventKind(kind streaming.EventKind) pluginapi.EventKind {
	switch kind {
	case streaming.KindTextDelta:
		return pluginapi.EventTextDelta
	case streaming.KindToolCallDelta:
		return pluginapi.EventToolCallDelta
	case streaming.KindReasoningDelta:
		return pluginapi.EventReasoningDelta
	case streaming.KindFinish:
		return pluginapi.EventFinish
	case streaming.KindUsage:
		return pluginapi.EventUsage
	default:
		return pluginapi.EventOther
	}
}

// terminationFor maps a blocking decision to the stream termination that
// renders it; allow and warn return nil.
func terminationFor(d pluginapi.Decision) *streaming.Termination {
	d = plugins.NormalizeDecision(d)
	switch d.Action {
	case pluginapi.ActionBlock:
		code := d.Code
		if code == "" {
			code = plugins.CodeBlocked
		}
		message := d.Message
		if message == "" {
			message = "response blocked by guardrail"
		}
		return &streaming.Termination{FinishReason: streaming.DefaultFinishReason, ErrorCode: code, ErrorMessage: message}
	case pluginapi.ActionRespond:
		return &streaming.Termination{FinishReason: "stop", Text: d.Response.Text(0)}
	default:
		return nil
	}
}

func concatChunks(chunks [][]byte) []byte {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}
