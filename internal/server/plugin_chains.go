package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/pluginapi"
)

// PluginChainsResolver resolves the per-request plugin chains (response and
// stream phases) selected by the matched workflow.
type PluginChainsResolver = guardrails.ContextChainsResolver

// pluginChainsFor returns the chains of the request, or nil when plugins are
// not wired or the workflow runs none.
func (s *translatedInferenceService) pluginChainsFor(ctx context.Context) *plugins.Chains {
	if s == nil || s.pluginChains == nil {
		return nil
	}
	return s.pluginChains.ChainsForContext(ctx)
}

// hasPostResponsePlugins reports whether the request runs response or stream
// phase plugins, which the native /v1/messages fast path cannot serve.
func (s *translatedInferenceService) hasPostResponsePlugins(ctx context.Context) bool {
	chains := s.pluginChainsFor(ctx)
	return chains != nil && (!chains.Response.Empty() || !chains.Stream.Empty())
}

// applyPluginResponseHeaders copies headers set by plugins (for example
// X-GoModel-Guardrail warnings) onto the client response.
func applyPluginResponseHeaders(c *echo.Context) {
	if state := plugins.RequestStateFromContext(c.Request().Context()); state != nil {
		state.ApplyResponseHeaders(c.Response().Header())
	}
}

// applyPluginRequestHeaders replays request header edits made by prompt-phase
// plugins onto the live request, so later middleware, the request log record, and
// passthrough forwarding see them.
func applyPluginRequestHeaders(c *echo.Context) {
	state := plugins.RequestStateFromContext(c.Request().Context())
	if state == nil {
		return
	}
	if changed := state.ApplyRequestHeaders(c.Request().Header); len(changed) > 0 {
		slog.Debug("plugins edited request headers", "request_id", core.GetRequestID(c.Request().Context()), "headers", changed)
	}
	// Prompt-phase plugins may also add headers to provider calls. Carry them
	// on the request context so every provider client of this request applies
	// them to its outgoing calls.
	if upstream := state.UpstreamHeaders(); len(upstream) > 0 {
		req := c.Request()
		*req = *req.WithContext(core.WithPluginUpstreamHeaders(req.Context(), upstream))
	}
}

// pluginDecisionDetail is the request-log-visible summary of one plugin decision.
type pluginDecisionDetail struct {
	Phase   string `json:"phase"`
	Action  string `json:"action"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  any    `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

// promptEditCaptureContext asks the prompt phase to keep a snapshot of every
// edit when the request is logged and step logging is on, so each editing
// step can be recorded as its own revision. Otherwise the phase keeps
// nothing.
func promptEditCaptureContext(c *echo.Context, requestLogger requestlog.LoggerInterface) context.Context {
	ctx := c.Request().Context()
	if !requestLogCaptureEnabled(requestLogger) || !requestLogger.Config().LogGuardrailSteps {
		return ctx
	}
	if entry, ok := c.Get(string(requestlog.LogEntryKey)).(*requestlog.LogEntry); !ok || entry == nil {
		return ctx
	}
	return plugins.WithPromptEditCapture(ctx)
}

// recordPromptPluginRevisions appends the prompt phase to the request log
// request-revision chain, one entry per instance that edited the prompt,
// objected (warn, block, respond) or failed, in step order. An editing step
// is a changed revision carrying the request as it stood right after that
// step, built from the snapshot the phase kept (see plugins.PromptEdit), so
// each step's revision shows what the next step worked on and the last one
// is what was forwarded. Building and encoding those requests runs off the
// request path; the request log entry collects the result when it is written. A
// body is stored only when body logging and revision-body logging are on
// and it fits the capture limit, matching the ingress rewriters. With step
// logging off (Config.LogGuardrailSteps) the phase kept no snapshots and
// its edits are one changed revision, naming the editing instances and
// carrying the request as forwarded (after). after is nil when nothing was
// forwarded (block, fail-closed, answered): the step snapshots, when kept,
// still show what each step handed on; without them the phase is recorded
// as decisions, an edit among them marked as a change without a body.
func recordPromptPluginRevisions(c *echo.Context, requestLogger requestlog.LoggerInterface, before, after any) {
	state := plugins.RequestStateFromContext(c.Request().Context())
	if state == nil {
		return
	}
	var records []plugins.DecisionRecord
	for _, record := range state.Snapshot() {
		if record.Phase != pluginapi.KindPrompt {
			continue
		}
		if record.Decision.Action == pluginapi.ActionAllow && !record.Edited && record.Err == nil {
			continue
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return
	}
	captureBodies := revisionBodiesEnabled(requestLogger)
	if edits := state.PromptEdits(); len(edits) > 0 {
		requestlog.EnrichEntryWithPendingRequestRevisions(c, func() []requestlog.RequestRevisionSnapshot {
			return promptStepRevisions(records, edits, encodeRequest(before), captureBodies)
		})
		return
	}
	edited := false
	for _, record := range records {
		edited = edited || record.Edited
	}
	if after != nil && edited {
		requestlog.EnrichEntryWithPendingRequestRevisions(c, func() []requestlog.RequestRevisionSnapshot {
			return promptChainRevisions(records, encodeRequest(before), encodeRequest(after), captureBodies)
		})
		return
	}
	size := len(encodeRequest(before))
	for _, record := range records {
		requestlog.EnrichEntryWithRequestRevision(c, requestlog.RequestRevisionSnapshot{
			Rewriter:    record.Instance,
			BytesBefore: size,
			BytesAfter:  size,
			NoChange:    !record.Edited,
			Detail:      decisionDetail(record),
		})
	}
}

// pluginEditDetail is the request-log-visible summary of a prompt chain's edits
// recorded as one revision: the instances that edited, in step order.
type pluginEditDetail struct {
	Phase  string   `json:"phase"`
	Edited []string `json:"edited"`
}

// promptChainRevisions builds the prompt phase's revisions without step
// snapshots: the objections as no-change entries, then one changed revision
// for the whole chain carrying the request as forwarded. Without the
// intermediate requests, an objection is sized as the original request
// until the first edit and as the forwarded one after it.
func promptChainRevisions(records []plugins.DecisionRecord, before, after []byte, captureBodies bool) []requestlog.RequestRevisionSnapshot {
	var revisions []requestlog.RequestRevisionSnapshot
	var editors []string
	size := len(before)
	for _, record := range records {
		if record.Edited {
			editors = append(editors, record.Instance)
			size = len(after)
		}
		if record.Decision.Action == pluginapi.ActionAllow && record.Err == nil {
			continue
		}
		revisions = append(revisions, requestlog.RequestRevisionSnapshot{Rewriter: record.Instance, BytesBefore: size, BytesAfter: size, NoChange: true, Detail: decisionDetail(record)})
	}
	revision := requestlog.RequestRevisionSnapshot{
		Rewriter:    strings.Join(editors, ", "),
		BytesBefore: len(before),
		BytesAfter:  len(after),
		Detail:      pluginEditDetail{Phase: string(pluginapi.KindPrompt), Edited: editors},
	}
	if captureBodies && len(after) > 0 && int64(len(after)) <= requestlog.MaxBodyCapture {
		revision.Body = requestlog.CaptureLoggedBody(after)
	}
	return append(revisions, revision)
}

// promptStepRevisions builds the prompt phase's revisions: each record in
// step order, an editing one as a change carrying the request after its
// step, measured against the request the step started from; a step that
// left the request alone reports that request's size on both sides. When a
// snapshot cannot be applied or encoded, the revision stays a change with
// the error in its detail and the size chain carries on unchanged.
func promptStepRevisions(records []plugins.DecisionRecord, edits []plugins.PromptEdit, before []byte, captureBodies bool) []requestlog.RequestRevisionSnapshot {
	revisions := make([]requestlog.RequestRevisionSnapshot, 0, len(records))
	previous := len(before)
	next := 0
	for _, record := range records {
		detail := decisionDetail(record)
		revision := requestlog.RequestRevisionSnapshot{Rewriter: record.Instance, NoChange: !record.Edited, BytesBefore: previous, BytesAfter: previous}
		if record.Edited && next < len(edits) && edits[next].Instance == record.Instance {
			edit := edits[next]
			next++
			var encoded []byte
			applied, err := edit.Apply()
			if err == nil {
				encoded = encodeRequest(applied)
			}
			if err != nil || encoded == nil {
				detail.Error = "request log snapshot of the edited request failed"
				if err != nil {
					detail.Error += ": " + err.Error()
				}
			}
			if encoded != nil {
				revision.BytesAfter = len(encoded)
				previous = len(encoded)
			}
			if captureBodies && len(encoded) > 0 && int64(len(encoded)) <= requestlog.MaxBodyCapture {
				revision.Body = requestlog.CaptureLoggedBody(encoded)
			}
		}
		revision.Detail = detail
		revisions = append(revisions, revision)
	}
	return revisions
}

// revisionBodiesEnabled reports whether revision bodies are stored at all:
// body logging and revision-body logging must both be on.
func revisionBodiesEnabled(requestLogger requestlog.LoggerInterface) bool {
	if !requestLogCaptureEnabled(requestLogger) {
		return false
	}
	cfg := requestLogger.Config()
	return cfg.LogBodies && cfg.LogRevisionBodies
}

func decisionDetail(record plugins.DecisionRecord) pluginDecisionDetail {
	detail := pluginDecisionDetail{
		Phase:   string(record.Phase),
		Action:  string(plugins.NormalizeDecision(record.Decision).Action),
		Code:    record.Decision.Code,
		Message: record.Decision.Message,
		Detail:  record.Decision.Detail,
	}
	if record.Err != nil {
		detail.Error = record.Err.Error()
	}
	return detail
}

// encodeRequest is the JSON form of a translated request, or nil when there
// is none or it does not encode.
func encodeRequest(v any) []byte {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// pluginMeta builds the exchange meta for a response phase, attempts included.
func pluginMeta(ctx context.Context, workflow *core.Workflow) pluginapi.Meta {
	meta := plugins.MetaFromContext(ctx, workflow)
	attempts := gateway.AttemptsFromContext(ctx)
	if len(attempts) == 0 {
		return meta
	}
	converted := make([]plugins.Attempt, 0, len(attempts))
	for _, attempt := range attempts {
		converted = append(converted, plugins.Attempt{
			Seq:          attempt.Seq,
			Kind:         attempt.Kind,
			ProviderType: attempt.ProviderType,
			ProviderName: attempt.ProviderName,
			Model:        attempt.Model,
			StatusCode:   attempt.StatusCode,
			Success:      attempt.Success,
			ErrorCode:    attempt.ErrorCode,
			Duration:     time.Duration(attempt.DurationNs),
		})
	}
	return plugins.WithAttempts(meta, converted)
}

// logResponseDecisions records response and stream phase decisions in the
// request state, which the request log entry's guardrail outcome trail is built
// from when it is written, and logs the objections with the request id.
func logResponseDecisions(requestID string, state *plugins.RequestState, records []plugins.DecisionRecord) {
	for _, record := range records {
		if record.Decision.Action == pluginapi.ActionAllow && record.Err == nil {
			continue
		}
		slog.Info("plugin decision",
			"request_id", requestID,
			"phase", string(record.Phase),
			"instance", record.Instance,
			"action", string(plugins.NormalizeDecision(record.Decision).Action),
			"code", record.Decision.Code,
			"error", record.Err,
		)
	}
	state.Record(records...)
}

// recordGuardrailOutcomes attaches the request's guardrail outcome trail to
// the request log entry when the workflow runs any plugin: the decisions known now
// (the prompt phase) go on the live entry, and the trail is rebuilt from the
// request state when the entry is written, so the response and stream
// phases, which finish after the handler returned, are included. It runs
// once the prepared workflow is on the request context.
func (s *translatedInferenceService) recordGuardrailOutcomes(c *echo.Context) {
	ctx := c.Request().Context()
	chains := s.pluginChainsFor(ctx)
	if chains == nil || (chains.Prompt.Empty() && chains.Response.Empty() && chains.Stream.Empty()) {
		return
	}
	requestlog.EnrichEntryWithGuardrailOutcomes(c, func() []requestlog.GuardrailOutcomeSnapshot {
		return guardrailOutcomes(plugins.RequestStateFromContext(ctx).Snapshot())
	})
}

// guardrailOutcomes converts the recorded decisions into the request log outcome
// trail, in the order they were recorded: phases run in sequence, so that is
// execution order. An instance that errored is a failure carrying its fail
// mode; its decision, allow by construction, is not reported.
func guardrailOutcomes(records []plugins.DecisionRecord) []requestlog.GuardrailOutcomeSnapshot {
	if len(records) == 0 {
		return nil
	}
	outcomes := make([]requestlog.GuardrailOutcomeSnapshot, 0, len(records))
	for _, record := range records {
		decision := plugins.NormalizeDecision(record.Decision)
		outcome := requestlog.GuardrailOutcomeSnapshot{
			Phase:          string(record.Phase),
			Step:           record.Step,
			Instance:       record.Instance,
			Type:           record.Type,
			Action:         string(decision.Action),
			Code:           decision.Code,
			Message:        decision.Message,
			Detail:         decision.Detail,
			Edited:         record.Edited,
			ReplacedEvents: record.Replaced,
			DroppedEvents:  record.Dropped,
			DurationNs:     record.Duration.Nanoseconds(),
		}
		if record.Err != nil {
			outcome.Action = requestlog.GuardrailActionFailure
			outcome.Error = record.Err.Error()
			outcome.FailMode = requestlog.GuardrailFailModeOpen
			if record.FailedClosed {
				outcome.FailMode = requestlog.GuardrailFailModeClosed
			}
		}
		if record.Edited {
			outcome.Target = requestlog.GuardrailTargetResponse
			if record.Phase == pluginapi.KindPrompt {
				outcome.Target = requestlog.GuardrailTargetRequest
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
