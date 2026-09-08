package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
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
// plugins onto the live request, so later middleware, the audit record, and
// passthrough forwarding see them.
func applyPluginRequestHeaders(c *echo.Context) {
	state := plugins.RequestStateFromContext(c.Request().Context())
	if state == nil {
		return
	}
	if changed := state.ApplyRequestHeaders(c.Request().Header); len(changed) > 0 {
		slog.Debug("plugins edited request headers", "request_id", core.GetRequestID(c.Request().Context()), "headers", changed)
	}
}

// pluginDecisionDetail is the audit-visible summary of one plugin decision.
type pluginDecisionDetail struct {
	Phase   string `json:"phase"`
	Action  string `json:"action"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  any    `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

// promptEditCaptureContext asks the prompt phase to keep a snapshot of every
// edit when the request is audited, so each editing step can be recorded as
// its own revision. Without an audit entry the phase keeps nothing.
func promptEditCaptureContext(c *echo.Context, auditLogger auditlog.LoggerInterface) context.Context {
	ctx := c.Request().Context()
	if !auditCaptureEnabled(auditLogger) {
		return ctx
	}
	if entry, ok := c.Get(string(auditlog.LogEntryKey)).(*auditlog.LogEntry); !ok || entry == nil {
		return ctx
	}
	return plugins.WithPromptEditCapture(ctx)
}

// recordPromptPluginRevisions appends the prompt phase to the audit
// request-revision chain, one entry per instance that edited the prompt,
// objected (warn, block, respond) or failed, in step order. An editing step
// is a changed revision carrying the request as it stood right after that
// step, built from the snapshot the phase kept (see plugins.PromptEdit), so
// each step's revision shows what the next step worked on and the last one
// is what was forwarded. Building and encoding those requests runs off the
// request path; the audit entry collects the result when it is written. A
// body is stored only when body logging and revision-body logging are on
// and it fits the capture limit, matching the ingress rewriters. after is
// nil when nothing was forwarded (block, fail-closed, answered): the phase
// is then recorded as decisions only.
func recordPromptPluginRevisions(c *echo.Context, auditLogger auditlog.LoggerInterface, before, after any) {
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
	edits := state.PromptEdits()
	if after == nil || len(edits) == 0 {
		for _, record := range records {
			auditlog.EnrichEntryWithRequestRevision(c, auditlog.RequestRevisionSnapshot{
				Rewriter: record.Instance,
				NoChange: true,
				Detail:   decisionDetail(record),
			})
		}
		return
	}
	captureBodies := revisionBodiesEnabled(auditLogger)
	auditlog.EnrichEntryWithPendingRequestRevisions(c, func() []auditlog.RequestRevisionSnapshot {
		return promptStepRevisions(records, edits, encodeRequest(before), captureBodies)
	})
}

// promptStepRevisions builds the prompt phase's revisions: each record in
// step order, an editing one as a change carrying the request after its
// step, measured against the request the step started from.
func promptStepRevisions(records []plugins.DecisionRecord, edits []plugins.PromptEdit, before []byte, captureBodies bool) []auditlog.RequestRevisionSnapshot {
	revisions := make([]auditlog.RequestRevisionSnapshot, 0, len(records))
	previous := len(before)
	next := 0
	for _, record := range records {
		detail := decisionDetail(record)
		revision := auditlog.RequestRevisionSnapshot{Rewriter: record.Instance, NoChange: !record.Edited}
		if record.Edited && next < len(edits) && edits[next].Instance == record.Instance {
			edit := edits[next]
			next++
			var encoded []byte
			applied, err := edit.Apply()
			if err == nil {
				encoded = encodeRequest(applied)
			}
			if err != nil || encoded == nil {
				detail.Error = "audit snapshot of the edited request failed"
				if err != nil {
					detail.Error += ": " + err.Error()
				}
			}
			revision.BytesBefore, revision.BytesAfter = previous, len(encoded)
			if encoded != nil {
				previous = len(encoded)
			}
			if captureBodies && len(encoded) > 0 && int64(len(encoded)) <= auditlog.MaxBodyCapture {
				revision.Body = auditlog.CaptureLoggedBody(encoded)
			}
		}
		revision.Detail = detail
		revisions = append(revisions, revision)
	}
	return revisions
}

// revisionBodiesEnabled reports whether revision bodies are stored at all:
// body logging and revision-body logging must both be on.
func revisionBodiesEnabled(auditLogger auditlog.LoggerInterface) bool {
	if !auditCaptureEnabled(auditLogger) {
		return false
	}
	cfg := auditLogger.Config()
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

// logResponseDecisions records response and stream phase outcomes: the audit
// revision chain is request-only, so these are logged with the request id.
func logResponseDecisions(requestID string, phase pluginapi.Kind, outcome plugins.Outcome, state *plugins.RequestState) {
	records := make([]plugins.DecisionRecord, 0, len(outcome.Records))
	for _, record := range outcome.Records {
		records = append(records, plugins.DecisionRecord{Phase: phase, Instance: record.Instance, Decision: record.Decision, Err: record.Err})
		if record.Decision.Action == pluginapi.ActionAllow && record.Err == nil {
			continue
		}
		slog.Info("plugin decision",
			"request_id", requestID,
			"phase", string(phase),
			"instance", record.Instance,
			"action", string(plugins.NormalizeDecision(record.Decision).Action),
			"code", record.Decision.Code,
			"error", record.Err,
		)
	}
	state.Record(records...)
}
