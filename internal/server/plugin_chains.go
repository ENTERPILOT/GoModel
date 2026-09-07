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

// recordPromptPluginRevisions appends the prompt-phase decisions to the audit
// request-revision chain. before and after are the request around the whole
// phase: the chain's edits are applied back to the request once, after every
// step has run, so the byte sizes describe the phase as a whole and the
// applied body is stored on the last editing instance's revision — the one
// whose output was forwarded. The body is captured only when body logging
// and revision-body logging are on and it fits the capture limit, matching
// the ingress rewriters.
func recordPromptPluginRevisions(c *echo.Context, auditLogger auditlog.LoggerInterface, before, after any) {
	state := plugins.RequestStateFromContext(c.Request().Context())
	if state == nil {
		return
	}
	var revisions []auditlog.RequestRevisionSnapshot
	lastEdited := -1
	for _, record := range state.Snapshot() {
		if record.Phase != pluginapi.KindPrompt {
			continue
		}
		if record.Decision.Action == pluginapi.ActionAllow && !record.Edited && record.Err == nil {
			continue
		}
		if record.Edited {
			lastEdited = len(revisions)
		}
		revisions = append(revisions, auditlog.RequestRevisionSnapshot{
			Rewriter: record.Instance,
			NoChange: !record.Edited,
			Detail:   decisionDetail(record),
		})
	}
	if lastEdited >= 0 {
		encodedBefore, encodedAfter := encodeRequest(before), encodeRequest(after)
		for i := range revisions {
			if !revisions[i].NoChange {
				revisions[i].BytesBefore, revisions[i].BytesAfter = len(encodedBefore), len(encodedAfter)
			}
		}
		if revisionBodyCaptureEnabled(auditLogger, len(encodedAfter)) {
			revisions[lastEdited].Body = auditlog.CaptureLoggedBody(encodedAfter)
		}
	}
	for _, revision := range revisions {
		auditlog.EnrichEntryWithRequestRevision(c, revision)
	}
}

// revisionBodyCaptureEnabled reports whether a revision body of the given
// size is stored: body logging and revision-body logging must be on and the
// body must be non-empty and within the audit capture limit.
func revisionBodyCaptureEnabled(auditLogger auditlog.LoggerInterface, size int) bool {
	if !auditCaptureEnabled(auditLogger) || size == 0 || int64(size) > auditlog.MaxBodyCapture {
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
