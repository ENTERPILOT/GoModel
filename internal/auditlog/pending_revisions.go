package auditlog

import (
	"log/slog"

	"github.com/labstack/echo/v5"
)

// pendingRequestRevisions is revision work running off the request path.
// The prompt-guardrail steps build and encode their request snapshots while
// the upstream call is in flight; the entry collects the result right
// before it is written (see LogEntry.CompleteRequestRevisions).
type pendingRequestRevisions struct {
	done      chan struct{}
	revisions []RequestRevisionSnapshot
}

func startPendingRequestRevisions(compute func() []RequestRevisionSnapshot) *pendingRequestRevisions {
	pending := &pendingRequestRevisions{done: make(chan struct{})}
	go func() {
		defer close(pending.done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("audit request revisions panicked", "panic", r)
			}
		}()
		pending.revisions = compute()
	}()
	return pending
}

// EnrichEntryWithPendingRequestRevisions appends the revisions compute
// returns to the live audit entry once they are ready. compute runs on its
// own goroutine so the request path never waits for it; the entry waits, if
// it still has to, only when it is about to be written. A missing entry is a
// no-op. A streamed entry created afterwards (CreateStreamEntry) shares the
// pending work.
func EnrichEntryWithPendingRequestRevisions(c *echo.Context, compute func() []RequestRevisionSnapshot) {
	entry := entryFromContext(c)
	if entry == nil || compute == nil {
		return
	}
	entry.pendingRevisions = append(entry.pendingRevisions, startPendingRequestRevisions(compute))
}

// Complete finishes the entry right before it is written: pending revision
// work is folded in and the guardrail outcomes are rebuilt from the phases
// that ran, the response and stream ones included.
func (e *LogEntry) Complete() {
	if e == nil {
		return
	}
	e.CompleteRequestRevisions()
	e.completeGuardrailOutcomes()
}

func (e *LogEntry) completeGuardrailOutcomes() {
	compute := e.guardrailOutcomes
	if compute == nil {
		return
	}
	e.guardrailOutcomes = nil
	e.setGuardrailOutcomes(compute())
}

func (e *LogEntry) setGuardrailOutcomes(outcomes []GuardrailOutcomeSnapshot) {
	outcomes = normalizeGuardrailOutcomes(outcomes)
	if len(outcomes) == 0 && (e.Data == nil || e.Data.Guardrails == nil) {
		return
	}
	ensureLogData(e).Guardrails = outcomes
}

// EnrichEntryWithGuardrailOutcomes records the guardrail outcomes compute
// returns on the live audit entry now, and again right before the entry is
// written, since the response and stream phases finish after the handler
// returned. compute must be safe to call from another goroutine at that
// point. A missing entry is a no-op; a streamed entry created afterwards
// (CreateStreamEntry) inherits the work.
func EnrichEntryWithGuardrailOutcomes(c *echo.Context, compute func() []GuardrailOutcomeSnapshot) {
	entry := entryFromContext(c)
	if entry == nil || compute == nil {
		return
	}
	entry.guardrailOutcomes = compute
	outcomes := compute()
	entry.setGuardrailOutcomes(outcomes)
	if len(outcomes) > 0 {
		publishLiveAuditUpdate(c, entry)
	}
}

// CompleteRequestRevisions waits for the entry's pending revision work, if
// any, and appends its results to the revision chain in sequence. It runs
// where the entry is about to be written and is a no-op afterwards.
func (e *LogEntry) CompleteRequestRevisions() {
	if e == nil || len(e.pendingRevisions) == 0 {
		return
	}
	pending := e.pendingRevisions
	e.pendingRevisions = nil
	for _, work := range pending {
		<-work.done
		if len(work.revisions) == 0 {
			continue
		}
		data := ensureLogData(e)
		for _, revision := range work.revisions {
			revision.Seq = len(data.RequestRevisions) + 1
			data.RequestRevisions = append(data.RequestRevisions, revision)
		}
	}
}
