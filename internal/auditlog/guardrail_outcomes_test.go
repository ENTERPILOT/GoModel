package auditlog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

// The outcome trail is recorded on the live entry when attached and rebuilt
// when the entry is written, so phases finishing after the handler returned
// (response, stream) are included; a streamed copy inherits the work.
func TestEnrichEntryWithGuardrailOutcomesRebuildsOnComplete(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
	entry := &LogEntry{ID: "entry-1"}
	c.Set(string(LogEntryKey), entry)

	outcomes := []GuardrailOutcomeSnapshot{{Phase: "prompt", Instance: " check ", Action: "WARN", Code: " pii "}}
	EnrichEntryWithGuardrailOutcomes(c, func() []GuardrailOutcomeSnapshot { return outcomes })
	if entry.Data == nil || len(entry.Data.Guardrails) != 1 {
		t.Fatalf("live entry outcomes = %+v, want the prompt outcome", entry.Data)
	}
	if got := entry.Data.Guardrails[0]; got.Seq != 1 || got.Instance != "check" || got.Action != GuardrailActionWarn || got.Code != "pii" {
		t.Fatalf("outcome = %+v, want normalized", got)
	}

	outcomes = append(outcomes, GuardrailOutcomeSnapshot{Phase: "response", Instance: "scrub", Action: "failure", Error: "boom", FailMode: "open", Edited: true, Target: "response"})
	streamed := CreateStreamEntry(context.Background(), entry)
	streamed.Complete()
	if len(streamed.Data.Guardrails) != 2 {
		t.Fatalf("streamed outcomes = %+v, want both phases", streamed.Data.Guardrails)
	}
	if got := streamed.Data.Guardrails[1]; got.Seq != 2 || got.Phase != "response" || got.FailMode != GuardrailFailModeOpen || got.Target != GuardrailTargetResponse {
		t.Fatalf("outcome = %+v, want the response failure", got)
	}
	// Completing twice does not rebuild again.
	outcomes = nil
	streamed.Complete()
	if len(streamed.Data.Guardrails) != 2 {
		t.Fatalf("outcomes after second complete = %+v, want unchanged", streamed.Data.Guardrails)
	}
}

func TestNormalizeGuardrailOutcomes(t *testing.T) {
	long := make([]byte, maxAttemptErrorMessageLength+10)
	for i := range long {
		long[i] = 'x'
	}
	got := normalizeGuardrailOutcomes([]GuardrailOutcomeSnapshot{
		{Instance: "", Action: "block"},
		{Instance: "a", Action: "bogus"},
		{Instance: "a", Action: "", Seq: 9, FailMode: "closed", Target: "request"},
		{Instance: "b", Action: "failure", Error: string(long), FailMode: "closed", Edited: true, Target: "request"},
	})
	if len(got) != 2 {
		t.Fatalf("outcomes = %+v, want the two valid ones", got)
	}
	if got[0].Seq != 1 || got[0].Action != GuardrailActionAllow || got[0].FailMode != "" || got[0].Target != "" {
		t.Errorf("outcome = %+v, want allow renumbered without fail mode or target", got[0])
	}
	if got[1].Seq != 2 || len(got[1].Error) != maxAttemptErrorMessageLength || got[1].FailMode != GuardrailFailModeClosed || got[1].Target != GuardrailTargetRequest {
		t.Errorf("outcome = %+v, want bounded error with fail mode and target", got[1])
	}
	if normalizeGuardrailOutcomes(nil) != nil {
		t.Error("no outcomes must stay nil")
	}
}

func TestCompleteWithoutOutcomesLeavesDataAlone(t *testing.T) {
	entry := &LogEntry{}
	entry.Complete()
	if entry.Data != nil {
		t.Fatalf("data = %+v, want none", entry.Data)
	}
	entry.guardrailOutcomes = func() []GuardrailOutcomeSnapshot { return nil }
	entry.Complete()
	if entry.Data != nil {
		t.Fatalf("data = %+v, want none when the trail is empty", entry.Data)
	}
	if err := errors.Join(); err != nil {
		t.Fatal(err)
	}
}
