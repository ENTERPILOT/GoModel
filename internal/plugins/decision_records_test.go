package plugins

import (
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestDecisionRecordsOf(t *testing.T) {
	failed := errors.New("boom")
	outcome := Outcome{Records: []Record{
		{Instance: "a", Type: "t", Step: 1, Decision: pluginapi.Warn("pii", "found", nil), Duration: time.Millisecond, Edited: true},
		{Instance: "b", Type: "t", Step: 2, Err: failed},
		{Instance: "c", Type: "t", Step: 2, Err: failed},
	}}
	records := DecisionRecordsOf(pluginapi.KindResponse, outcome, &PluginError{Instance: "c", Phase: pluginapi.KindResponse, Err: failed})
	if len(records) != 3 {
		t.Fatalf("records = %+v, want 3", records)
	}
	first := records[0]
	if first.Phase != pluginapi.KindResponse || first.Instance != "a" || first.Type != "t" || first.Step != 1 ||
		first.Decision.Action != pluginapi.ActionWarn || first.Duration != time.Millisecond || !first.Edited || first.FailedClosed {
		t.Errorf("record = %+v, want the warn record copied", first)
	}
	if records[1].Err != failed || records[1].FailedClosed {
		t.Errorf("record = %+v, want a fail-open failure", records[1])
	}
	if records[2].Err != failed || !records[2].FailedClosed {
		t.Errorf("record = %+v, want the failure that ended the run marked closed", records[2])
	}
	if got := DecisionRecordsOf(pluginapi.KindPrompt, outcome, errors.New("not a plugin error")); got[2].FailedClosed {
		t.Errorf("record = %+v, want no fail-closed mark without a plugin error", got[2])
	}
}
