package server

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// outcomeDefinition is one guardrail instance of the phase_test plugin with
// its own fail mode.
type outcomeDefinition struct {
	name     string
	cfg      map[string]string
	failMode string
}

func outcomeChains(t *testing.T, definitions []outcomeDefinition, steps ...guardrails.StepReference) *plugins.Chains {
	t.Helper()
	defs := make([]guardrails.Definition, 0, len(definitions))
	for _, def := range definitions {
		raw, _ := json.Marshal(def.cfg)
		defs = append(defs, guardrails.Definition{Name: def.name, Type: "phase_test", Config: raw, FailMode: def.failMode})
	}
	return newGuardrailChains(t, nil, steps, []func() pluginapi.Plugin{newPhasePlugin}, defs...)
}

// runGuardrailOutcomes runs one chat request through the chains and returns
// the guardrail outcome trail of the audit entry as it is written.
func runGuardrailOutcomes(t *testing.T, body string, chains *plugins.Chains) (int, []auditlog.GuardrailOutcomeSnapshot) {
	t.Helper()
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true}}
	handler := phaseHandlerWithLogger(t, phaseProvider(), auditLogger, chains)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := chatContext(t, body, echotest.WithValue(string(auditlog.LogEntryKey), entry))
	err := handler.ChatCompletion(c)
	require.NoError(t, err)

	if len(auditLogger.entries) > 0 {
		// A streamed request is written by the stream observer.
		return rec.Code, auditLogger.entries[0].Data.Guardrails
	}
	entry.Complete()
	return rec.Code, entry.Data.Guardrails
}

// Every guardrail that ran leaves an outcome in the audit entry, silent
// allows included, in execution order across phases, with what it decided,
// whether it edited, and how it failed.
func TestChatCompletion_GuardrailOutcomes(t *testing.T) {
	single := func(cfg map[string]string, phase pluginapi.Kind) (definitions []outcomeDefinition, steps []guardrails.StepReference) {
		return []outcomeDefinition{{name: "phase", cfg: cfg}}, []guardrails.StepReference{{Ref: "phase", Phase: phase, Step: 1}}
	}
	type want struct {
		phase    string
		instance string
		action   string
		code     string
		edited   bool
		target   string
		failMode string
		replaced bool
	}
	tests := []struct {
		name        string
		body        string
		definitions []outcomeDefinition
		steps       []guardrails.StepReference
		wantStatus  int
		want        []want
	}{
		{name: "prompt allow", body: chatBody, wantStatus: 200,
			want: []want{{phase: "prompt", instance: "phase", action: "allow"}}},
		{name: "prompt edit", body: chatBody, wantStatus: 200,
			want: []want{{phase: "prompt", instance: "phase", action: "allow", edited: true, target: "request"}}},
		{name: "prompt warn", body: chatBody, wantStatus: 200,
			want: []want{{phase: "prompt", instance: "phase", action: "warn", code: "pii"}}},
		{name: "prompt block", body: chatBody, wantStatus: 400,
			want: []want{{phase: "prompt", instance: "phase", action: "block", code: "policy"}}},
		{name: "prompt respond", body: chatBody, wantStatus: 200,
			want: []want{{phase: "prompt", instance: "phase", action: "respond"}}},
		{name: "prompt edit then fail closed", body: chatBody, wantStatus: 500,
			want: []want{{phase: "prompt", instance: "phase", action: "failure", edited: true, target: "request", failMode: "closed"}}},
		{name: "prompt fail open", body: chatBody, wantStatus: 200,
			definitions: []outcomeDefinition{{name: "phase", cfg: map[string]string{"prompt": "fail"}, failMode: "open"}},
			steps:       []guardrails.StepReference{{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1}},
			want:        []want{{phase: "prompt", instance: "phase", action: "failure", failMode: "open"}}},
		{name: "response edit", body: chatBody, wantStatus: 200,
			want: []want{{phase: "response", instance: "phase", action: "allow", edited: true, target: "response"}}},
		{name: "response block", body: chatBody, wantStatus: 502,
			want: []want{{phase: "response", instance: "phase", action: "block", code: "policy"}}},
		{name: "response fail closed", body: chatBody, wantStatus: 500,
			want: []want{{phase: "response", instance: "phase", action: "failure", failMode: "closed"}}},
		{name: "stream replace", body: chatStreamBody, wantStatus: 200,
			want: []want{{phase: "stream", instance: "phase", action: "allow", edited: true, target: "response", replaced: true}}},
		{name: "stream terminate", body: chatStreamBody, wantStatus: 200,
			want: []want{{phase: "stream", instance: "phase", action: "block", code: "policy"}}},
		{name: "stream end block", body: chatStreamBody, wantStatus: 200,
			want: []want{{phase: "stream", instance: "phase", action: "block", code: "policy"}}},
		{name: "buffered stream instance is a stream outcome", body: chatStreamBody, wantStatus: 200,
			want: []want{{phase: "stream", instance: "phase", action: "allow", edited: true, target: "response"}}},
		{name: "response block on a stream", body: chatStreamBody, wantStatus: 200,
			want: []want{{phase: "response", instance: "phase", action: "block", code: "policy"}}},
		{name: "stream event fail open", body: chatStreamBody, wantStatus: 200,
			definitions: []outcomeDefinition{{name: "phase", cfg: map[string]string{"stream": "fail_event"}, failMode: "open"}},
			steps:       []guardrails.StepReference{{Ref: "phase", Phase: pluginapi.KindStream, Step: 1}},
			want:        []want{{phase: "stream", instance: "phase", action: "failure", failMode: "open"}}},
		{name: "stream event fail closed", body: chatStreamBody, wantStatus: 200,
			definitions: []outcomeDefinition{{name: "phase", cfg: map[string]string{"stream": "fail_event"}}},
			steps:       []guardrails.StepReference{{Ref: "phase", Phase: pluginapi.KindStream, Step: 1}},
			want:        []want{{phase: "stream", instance: "phase", action: "failure", failMode: "closed"}}},
		{name: "instance in both response and buffered stream phases", body: chatStreamBody, wantStatus: 200,
			definitions: []outcomeDefinition{{name: "phase", cfg: map[string]string{"stream": "buffer", "response": "edit", "text": "assembled"}}},
			steps: []guardrails.StepReference{
				{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1},
				{Ref: "phase", Phase: pluginapi.KindStream, Step: 1},
			},
			want: []want{
				{phase: "response", instance: "phase", action: "allow", edited: true, target: "response"},
				{phase: "stream", instance: "phase", action: "allow", edited: true, target: "response"},
			}},
		{name: "prompt then response", body: chatBody, wantStatus: 200,
			definitions: []outcomeDefinition{
				{name: "check", cfg: map[string]string{"prompt": "warn"}},
				{name: "scrub", cfg: map[string]string{"response": "edit", "text": "redacted"}},
			},
			steps: []guardrails.StepReference{
				{Ref: "check", Phase: pluginapi.KindPrompt, Step: 1},
				{Ref: "scrub", Phase: pluginapi.KindResponse, Step: 1},
			},
			want: []want{
				{phase: "prompt", instance: "check", action: "warn", code: "pii"},
				{phase: "response", instance: "scrub", action: "allow", edited: true, target: "response"},
			}},
		{name: "prompt block skips the response phase", body: chatBody, wantStatus: 400,
			definitions: []outcomeDefinition{
				{name: "gate", cfg: map[string]string{"prompt": "block"}},
				{name: "scrub", cfg: map[string]string{"response": "edit", "text": "redacted"}},
			},
			steps: []guardrails.StepReference{
				{Ref: "gate", Phase: pluginapi.KindPrompt, Step: 1},
				{Ref: "scrub", Phase: pluginapi.KindResponse, Step: 1},
			},
			want: []want{{phase: "prompt", instance: "gate", action: "block", code: "policy"}}},
	}
	singleCfg := map[string][2]any{
		"prompt allow":                 {map[string]string{}, pluginapi.KindPrompt},
		"prompt edit":                  {map[string]string{"prompt": "edit", "text": "rewritten"}, pluginapi.KindPrompt},
		"prompt warn":                  {map[string]string{"prompt": "warn"}, pluginapi.KindPrompt},
		"prompt block":                 {map[string]string{"prompt": "block"}, pluginapi.KindPrompt},
		"prompt respond":               {map[string]string{"prompt": "respond", "text": "canned"}, pluginapi.KindPrompt},
		"prompt edit then fail closed": {map[string]string{"prompt": "edit_fail", "text": "rewritten"}, pluginapi.KindPrompt},
		"response edit":                {map[string]string{"response": "edit", "text": "redacted"}, pluginapi.KindResponse},
		"response block":               {map[string]string{"response": "block"}, pluginapi.KindResponse},
		"response fail closed":         {map[string]string{"response": "fail"}, pluginapi.KindResponse},
		"stream replace":               {map[string]string{"stream": "replace", "text": "[x]"}, pluginapi.KindStream},
		"stream terminate":             {map[string]string{"stream": "terminate"}, pluginapi.KindStream},
		"stream end block":             {map[string]string{"stream": "end_block"}, pluginapi.KindStream},
		"buffered stream instance is a stream outcome": {map[string]string{"stream": "buffer", "response": "edit", "text": "assembled"}, pluginapi.KindStream},
		"response block on a stream":                   {map[string]string{"response": "block"}, pluginapi.KindResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definitions, steps := tt.definitions, tt.steps
			if definitions == nil {
				cfg := singleCfg[tt.name]
				definitions, steps = single(cfg[0].(map[string]string), cfg[1].(pluginapi.Kind))
			}
			status, outcomes := runGuardrailOutcomes(t, tt.body, outcomeChains(t, definitions, steps...))
			require.Equal(t, tt.wantStatus, status)
			require.Len(t, outcomes, len(tt.want))

			for i, want := range tt.want {
				got := outcomes[i]
				assert.Equal(t, i+1, got.Seq)
				assert.Equal(t, 1, got.Step)
				assert.Equal(t, "phase_test", got.Type, "outcome %d = %+v, want seq %d, step 1, type phase_test", i, got, i+1)
				assert.Equal(t, want.phase, got.Phase)
				assert.Equal(t, want.instance, got.Instance)
				assert.Equal(t, want.action, got.Action)
				assert.Equal(t, want.code, got.Code, "outcome %d = %+v, want %+v", i, got, want)
				assert.Equal(t, want.edited, got.Edited)
				assert.Equal(t, want.target, got.Target)
				assert.Equal(t, want.failMode, got.FailMode, "outcome %d = %+v, want edited %v target %q fail mode %q", i, got, want.edited, want.target, want.failMode)
				assert.Equal(t, got.Error != "", got.Action == auditlog.GuardrailActionFailure, "outcome %d = %+v: a failure carries its error and nothing else does", i, got)
				assert.Equal(t, want.replaced, got.ReplacedEvents > 0, "outcome %d = %+v, want replaced events %v", i, got, want.replaced)

				if got.Phase == "stream" {
					assert.NotZero(t, got.DurationNs, "outcome %d = %+v, want the stream hooks' time", i, got)
				}
			}
		})
	}
}

// A workflow without plugins leaves no outcome trail on the entry, not even
// an empty one.
func TestChatCompletion_NoGuardrailsNoOutcomes(t *testing.T) {
	_, outcomes := runGuardrailOutcomes(t, chatBody, outcomeChains(t, nil))
	require.Nil(t, outcomes)
}

func TestGuardrailOutcomesFromRecords(t *testing.T) {
	records := []plugins.DecisionRecord{
		{Phase: pluginapi.KindPrompt, Instance: "a", Type: "t", Step: 2, Decision: pluginapi.Warn("pii", "found", map[string]int{"hits": 1}), Edited: true},
		{Phase: pluginapi.KindStream, Instance: "b", Err: errors.New("failed"), FailedClosed: true, Replaced: 2, Dropped: 1, Edited: true},
		{Phase: pluginapi.KindResponse, Instance: "c", Decision: pluginapi.Decision{}},
	}
	got := guardrailOutcomes(records)
	want := []auditlog.GuardrailOutcomeSnapshot{
		{Phase: "prompt", Instance: "a", Type: "t", Step: 2, Action: "warn", Code: "pii", Message: "found", Detail: map[string]int{"hits": 1}, Edited: true, Target: "request"},
		{Phase: "stream", Instance: "b", Action: "failure", Error: "failed", FailMode: "closed", Edited: true, Target: "response", ReplacedEvents: 2, DroppedEvents: 1},
		{Phase: "response", Instance: "c", Action: "allow"},
	}
	require.Len(t, got, len(want))

	for i := range want {
		g, w := got[i], want[i]
		assert.Equal(t, w.Phase, g.Phase)
		assert.Equal(t, w.Instance, g.Instance)
		assert.Equal(t, w.Type, g.Type)
		assert.Equal(t, w.Step, g.Step)
		assert.Equal(t, w.Action, g.Action)
		assert.Equal(t, w.Code, g.Code)
		assert.Equal(t, w.Message, g.Message)
		assert.Equal(t, w.Error, g.Error)
		assert.Equal(t, w.FailMode, g.FailMode)
		assert.Equal(t, w.Edited, g.Edited)
		assert.Equal(t, w.Target, g.Target)
		assert.Equal(t, w.ReplacedEvents, g.ReplacedEvents)
		assert.Equal(t, w.DroppedEvents, g.DroppedEvents, "outcome %d = %+v, want %+v", i, g, w)
	}
	assert.NotNil(t, got[0].Detail, "decision detail must be kept: %+v", got[0])
}
