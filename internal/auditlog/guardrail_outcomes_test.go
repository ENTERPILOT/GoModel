package auditlog

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The outcome trail is recorded on the live entry when attached and rebuilt
// when the entry is written, so phases finishing after the handler returned
// (response, stream) are included; a streamed copy inherits the work.
func TestEnrichEntryWithGuardrailOutcomesRebuildsOnComplete(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &LogEntry{ID: "entry-1"}
	c.Set(string(LogEntryKey), entry)

	outcomes := []GuardrailOutcomeSnapshot{{Phase: "prompt", Instance: " check ", Action: "WARN", Code: " pii "}}
	EnrichEntryWithGuardrailOutcomes(c, func() []GuardrailOutcomeSnapshot { return outcomes })
	require.NotNil(t, entry.Data)
	require.Len(t, entry.Data.Guardrails, 1)
	got := entry.Data.Guardrails[0]
	require.Equal(t, 1, got.Seq)
	require.Equal(t, "check", got.Instance)
	require.Equal(t, GuardrailActionWarn, got.Action)
	require.Equal(t, "pii", got.Code)

	outcomes = append(outcomes, GuardrailOutcomeSnapshot{Phase: "response", Instance: "scrub", Action: "failure", Error: "boom", FailMode: "open", Edited: true, Target: "response"})
	streamed := CreateStreamEntry(context.Background(), entry)
	streamed.Complete()
	require.Len(t, streamed.Data.Guardrails, 2)
	got = streamed.Data.Guardrails[1]
	require.Equal(t, 2, got.Seq)
	require.Equal(t, "response", got.Phase)
	require.Equal(t, GuardrailFailModeOpen, got.FailMode)
	require.Equal(t, GuardrailTargetResponse, got.Target)

	// Completing twice does not rebuild again.
	outcomes = nil
	streamed.Complete()
	require.Len(t, streamed.Data.Guardrails, 2)
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
	require.Len(t, got, 2)
	assert.Equal(t, 1, got[0].Seq)
	assert.Equal(t, GuardrailActionAllow, got[0].Action)
	assert.Empty(t, got[0].FailMode)
	assert.Empty(t, got[0].Target)
	assert.Equal(t, 2, got[1].Seq)
	assert.Len(t, got[1].Error, maxAttemptErrorMessageLength)
	assert.Equal(t, GuardrailFailModeClosed, got[1].FailMode)
	assert.Equal(t, GuardrailTargetRequest, got[1].Target)
	assert.Nil(t, normalizeGuardrailOutcomes(nil))
}

func TestCompleteWithoutOutcomesLeavesDataAlone(t *testing.T) {
	entry := &LogEntry{}
	entry.Complete()
	require.Nil(t, entry.Data)

	entry.guardrailOutcomes = func() []GuardrailOutcomeSnapshot { return nil }
	entry.Complete()
	require.Nil(t, entry.Data)
}
