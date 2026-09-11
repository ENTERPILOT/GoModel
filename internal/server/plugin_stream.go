package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"
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

	// One buffer serves every buffered instance and the response chain. Its
	// cap is the largest one asked for, and the host default as soon as any
	// participant (the response chain always) asks for none, so one
	// instance's small cap cannot fail every long answer for the others.
	var buffered []*plugins.Instance
	maxBuffer := 0
	uncapped := !chains.Response.Empty()
	for _, inst := range chains.Stream.Instances() {
		policy := inst.StreamPolicy()
		if policy.Mode == pluginapi.StreamBuffer {
			buffered = append(buffered, inst)
			if policy.MaxBufferBytes <= 0 {
				uncapped = true
			}
			maxBuffer = max(maxBuffer, policy.MaxBufferBytes)
			continue
		}
		ps.inFlight = append(ps.inFlight, inFlightInstance{inst: inst, observe: policy.Mode != pluginapi.StreamTransform})
		if policy.Mode == pluginapi.StreamTransform {
			ps.lookbehind = max(ps.lookbehind, policy.LookbehindChars)
			ps.minChunk = max(ps.minChunk, policy.MinChunkChars)
		}
	}

	if uncapped {
		maxBuffer = 0
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
			MinChunkChars:   ps.minChunk,
			OnError:         ps.reportError("transform"),
		})
	}
	// The instances stay held until the stream is closed, however long it
	// runs, so a guardrail replaced meanwhile is not closed underneath it.
	chains.Acquire()
	return &releaseOnClose{ReadCloser: stream, release: chains.Release}
}

// releaseOnClose runs release once when the stream is closed.
type releaseOnClose struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
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
	minChunk   int
	// replaced and dropped count, per in-flight instance, the events it
	// rewrote or withheld; eventTime is the time its event hook took over
	// the stream and failedOpen its first event failure the chain carried on
	// from. All feed the audit outcome trail.
	replaced   map[string]int
	dropped    map[string]int
	eventTime  map[string]time.Duration
	failedOpen map[string]error
}

func (ps *pluginStream) countEdit(counts *map[string]int, instance string) {
	if *counts == nil {
		*counts = map[string]int{}
	}
	(*counts)[instance]++
}

func (ps *pluginStream) addEventTime(instance string, d time.Duration) {
	if ps.eventTime == nil {
		ps.eventTime = map[string]time.Duration{}
	}
	ps.eventTime[instance] += d
}

func (ps *pluginStream) noteFailedOpen(instance string, err error) {
	if ps.failedOpen == nil {
		ps.failedOpen = map[string]error{}
	}
	if _, seen := ps.failedOpen[instance]; !seen {
		ps.failedOpen[instance] = err
	}
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
	// appended are the buffered instances that joined the chain here, as
	// opposed to those already in the response chain.
	var appended []*plugins.Instance
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
				appended = append(appended, inst)
			}
		}
		merged, err := plugins.BuildChain(pluginapi.KindResponse, refs)
		if err != nil {
			return plugins.Outcome{}, err
		}
		chain = merged
	}
	outcome, err := chain.RunResponse(ps.ctx, ps.x)
	if !plugins.Abandoned(err) { // an abandoned mutator may still write ps.x
		ps.state.Finish(ps.x)
	}
	// A buffered stream instance ran its response hook here, but it is a
	// stream-phase step of the workflow and is recorded as one. An instance
	// configured in both phases ran once for both steps: its response
	// record stays, and a copy stands for the stream step.
	records := plugins.DecisionRecordsOf(pluginapi.KindResponse, outcome, err)
	for i := range records {
		if slices.Contains(instanceNames(appended), records[i].Instance) {
			records[i].Phase = pluginapi.KindStream
			records[i].Step = ps.streamStep(records[i].Instance)
		}
	}
	for _, inst := range buffered {
		if slices.Contains(appended, inst) {
			continue
		}
		for _, record := range records {
			if record.Instance == inst.Name && record.Phase == pluginapi.KindResponse {
				record.Phase = pluginapi.KindStream
				record.Step = ps.streamStep(inst.Name)
				records = append(records, record)
				break
			}
		}
	}
	logResponseDecisions(ps.requestID, ps.state, records)
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

func instanceNames(instances []*plugins.Instance) []string {
	names := make([]string, 0, len(instances))
	for _, inst := range instances {
		names = append(names, inst.Name)
	}
	return names
}

// OnEvent runs the in-flight stream instances over one event in step order.
// A replace feeds the next instance; drop and terminate end the walk. The
// exchange stream state records the event as the client receives it, after
// the walk, so a later instance's OnStreamEnd sees replaced and dropped
// text that way rather than the original.
func (ps *pluginStream) OnEvent(ev *streaming.Event) (streaming.Decision, error) {
	pev := &pluginapi.StreamEvent{Seq: ev.Seq + 1, Kind: pluginEventKind(ev.Kind), Choice: ev.Choice, Call: ev.Call, Text: ev.Text, Overlap: ev.Overlap, Raw: ev.Data}
	result := streaming.Decision{Action: streaming.ActionPass}
	for _, entry := range ps.inFlight {
		inst, observe := entry.inst, entry.observe
		hook, ok := inst.Plugin.(pluginapi.StreamHook)
		if !ok {
			continue
		}
		start := time.Now()
		decision, err := plugins.Call(ps.ctx, inst, func(ctx context.Context) (pluginapi.StreamDecision, error) {
			return hook.OnStreamEvent(ctx, ps.x, pev)
		})
		ps.addEventTime(inst.Name, time.Since(start))
		if err != nil {
			if inst.FailsOpen(pluginapi.KindStream, err, true) {
				slog.Warn("stream plugin failed; continuing (fail_open)", "request_id", ps.requestID, "instance", inst.Name, "error", err)
				ps.noteFailedOpen(inst.Name, err)
				continue
			}
			ps.state.Record(ps.streamRecord(inst, plugins.DecisionRecord{Err: err, FailedClosed: true}))
			return streaming.Decision{}, &plugins.PluginError{Instance: inst.Name, Phase: pluginapi.KindStream, Err: err}
		}
		switch decision.Action {
		case pluginapi.StreamTerminate:
			var term pluginapi.Decision
			if decision.Terminate != nil {
				term = *decision.Terminate
			}
			ps.state.Record(ps.streamRecord(inst, plugins.DecisionRecord{Decision: term}))
			return streaming.Decision{Action: streaming.ActionTerminate, Terminate: terminationFor(term)}, nil
		case pluginapi.StreamDrop:
			if observe {
				continue
			}
			ps.countEdit(&ps.dropped, inst.Name)
			ps.x.Stream.ReplaceTail(pev, ev.Overlap, "")
			return streaming.Decision{Action: streaming.ActionDrop}, nil
		case pluginapi.StreamReplace:
			if observe || !replaceable(pev.Kind) {
				continue
			}
			ps.countEdit(&ps.replaced, inst.Name)
			pev.Text = decision.Text
			result = streaming.Decision{Action: streaming.ActionReplace, Text: decision.Text}
		}
	}
	if result.Action == streaming.ActionReplace {
		// The replacement covers the whole window: the withheld tail
		// already recorded plus this event's fresh text.
		ps.x.Stream.ReplaceTail(pev, ev.Overlap, result.Text)
	} else {
		ps.appendState(pev, ev.Overlap)
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
	records := plugins.DecisionRecordsOf(pluginapi.KindStream, outcome, err)
	for i := range records {
		ps.applyStreamEdits(&records[i])
	}
	logResponseDecisions(ps.requestID, ps.state, records)
	ps.recordWarn(outcome)
	if err != nil {
		return nil, err
	}
	return terminationFor(outcome.Decision), nil
}

// streamRecord is a stream-phase decision record of an in-flight instance
// taken mid-stream (a terminate or a failure), with its edits so far.
func (ps *pluginStream) streamRecord(inst *plugins.Instance, record plugins.DecisionRecord) plugins.DecisionRecord {
	record.Phase = pluginapi.KindStream
	record.Instance = inst.Name
	record.Type = inst.Type
	ps.applyStreamEdits(&record)
	return record
}

// applyStreamEdits folds the instance's event work into its record: a
// replaced or dropped event is an edit of the response, the event hooks'
// time counts toward its duration, and an event failure the chain carried
// on from makes a record that decided nothing a fail-open failure. The step
// is the instance's configured one: the in-flight instances run on ad hoc
// chains whose steps are positions, not the workflow's.
func (ps *pluginStream) applyStreamEdits(record *plugins.DecisionRecord) {
	record.Step = ps.streamStep(record.Instance)
	record.Replaced = ps.replaced[record.Instance]
	record.Dropped = ps.dropped[record.Instance]
	record.Edited = record.Edited || record.Replaced > 0 || record.Dropped > 0
	record.Duration += ps.eventTime[record.Instance]
	if err := ps.failedOpen[record.Instance]; err != nil && record.Err == nil && plugins.NormalizeDecision(record.Decision).Action == pluginapi.ActionAllow {
		record.Err = err
	}
}

// streamStep is the configured step of a stream-phase instance, or zero when
// it is not in the stream chain.
func (ps *pluginStream) streamStep(instance string) int {
	for _, step := range ps.chains.Stream.StepsOf() {
		for _, inst := range step.Instances {
			if inst.Name == instance {
				return step.Order
			}
		}
	}
	return 0
}

// recordWarn adds the warn header for a warn outcome. It reaches the client
// only when the headers are not committed yet: a buffered stream that
// finished before its first keep-alive. Later warns stay in the audit trail.
func (ps *pluginStream) recordWarn(outcome plugins.Outcome) {
	if outcome.Decision.Action == pluginapi.ActionWarn {
		ps.state.AddResponseHeader(plugins.GuardrailHeader, plugins.WarnHeaderValue(outcome.Decision))
	}
}

// appendState records the new text of a passed event in the exchange stream
// state. Under lookbehind the event repeats overlap runes already seen, so
// only the rest is fresh.
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

// replaceable reports whether a replace decision applies to the kind: text,
// reasoning, and tool-call argument deltas.
func replaceable(kind pluginapi.EventKind) bool {
	return kind == pluginapi.EventTextDelta || kind == pluginapi.EventReasoningDelta || kind == pluginapi.EventToolCallDelta
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
