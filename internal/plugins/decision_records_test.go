package plugins

import (
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionRecordsOf(t *testing.T) {
	failed := errors.New("boom")
	outcome := Outcome{Records: []Record{
		{Instance: "a", Type: "t", Step: 1, Decision: pluginapi.Warn("pii", "found", nil), Duration: time.Millisecond, Edited: true},
		{Instance: "b", Type: "t", Step: 2, Err: failed},
		{Instance: "c", Type: "t", Step: 2, Err: failed},
	}}
	records := DecisionRecordsOf(pluginapi.KindResponse, outcome, &PluginError{Instance: "c", Phase: pluginapi.KindResponse, Err: failed})
	require.Len(t, records, 3)

	first := records[0]
	assert.Equal(t, pluginapi.KindResponse, first.Phase)
	assert.Equal(t, "a", first.Instance)
	assert.Equal(t, "t", first.Type)
	assert.Equal(t, 1, first.Step)
	assert.Equal(t, pluginapi.ActionWarn, first.Decision.Action)
	assert.Equal(t, time.Millisecond, first.Duration)
	assert.True(t, first.Edited)
	assert.False(t, first.FailedClosed, "record = %+v, want the warn record copied", first)
	assert.Equal(t, failed, records[1].Err)
	assert.False(t, records[1].FailedClosed, "record = %+v, want a fail-open failure", records[1])
	assert.Equal(t, failed, records[2].Err)
	assert.True(t, records[2].FailedClosed, "record = %+v, want the failure that ended the run marked closed", records[2])
	got := DecisionRecordsOf(pluginapi.KindPrompt, outcome, errors.New("not a plugin error"))
	assert.False(t, got[2].FailedClosed, "record = %+v, want no fail-closed mark without a plugin error", got[2])
}
