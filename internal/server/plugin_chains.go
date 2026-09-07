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
// request-revision chain. before and after are the encoded request around
// the phase; they are only measured when an instance edited the request.
func recordPromptPluginRevisions(c *echo.Context, before, after any) {
	state := plugins.RequestStateFromContext(c.Request().Context())
	if state == nil {
		return
	}
	var bytesBefore, bytesAfter int
	measured := false
	for _, record := range state.Snapshot() {
		if record.Phase != pluginapi.KindPrompt {
			continue
		}
		if record.Decision.Action == pluginapi.ActionAllow && !record.Edited && record.Err == nil {
			continue
		}
		revision := auditlog.RequestRevisionSnapshot{Rewriter: record.Instance, NoChange: !record.Edited, Detail: decisionDetail(record)}
		if record.Edited {
			if !measured {
				bytesBefore, bytesAfter = encodedSize(before), encodedSize(after)
				measured = true
			}
			revision.BytesBefore, revision.BytesAfter = bytesBefore, bytesAfter
		}
		auditlog.EnrichEntryWithRequestRevision(c, revision)
	}
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

func encodedSize(v any) int {
	if v == nil {
		return 0
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
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
