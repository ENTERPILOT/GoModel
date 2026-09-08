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
