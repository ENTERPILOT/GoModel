package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
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
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(body), false, "", nil)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
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
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if len(outcomes) != len(tt.want) {
				t.Fatalf("outcomes = %+v, want %d", outcomes, len(tt.want))
			}
			for i, want := range tt.want {
				got := outcomes[i]
				if got.Seq != i+1 || got.Step != 1 || got.Type != "phase_test" {
					t.Errorf("outcome %d = %+v, want seq %d, step 1, type phase_test", i, got, i+1)
				}
				if got.Phase != want.phase || got.Instance != want.instance || got.Action != want.action || got.Code != want.code {
					t.Errorf("outcome %d = %+v, want %+v", i, got, want)
				}
				if got.Edited != want.edited || got.Target != want.target || got.FailMode != want.failMode {
					t.Errorf("outcome %d = %+v, want edited %v target %q fail mode %q", i, got, want.edited, want.target, want.failMode)
				}
				if (got.Action == auditlog.GuardrailActionFailure) != (got.Error != "") {
					t.Errorf("outcome %d = %+v: a failure carries its error and nothing else does", i, got)
				}
				if want.replaced != (got.ReplacedEvents > 0) {
					t.Errorf("outcome %d = %+v, want replaced events %v", i, got, want.replaced)
				}
			}
		})
	}
}

// A workflow without plugins leaves no outcome trail on the entry, not even
// an empty one.
func TestChatCompletion_NoGuardrailsNoOutcomes(t *testing.T) {
	_, outcomes := runGuardrailOutcomes(t, chatBody, outcomeChains(t, nil))
	if outcomes != nil {
		t.Fatalf("outcomes = %+v, want none", outcomes)
	}
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
	if len(got) != len(want) {
		t.Fatalf("outcomes = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Phase != w.Phase || g.Instance != w.Instance || g.Type != w.Type || g.Step != w.Step || g.Action != w.Action ||
			g.Code != w.Code || g.Message != w.Message || g.Error != w.Error || g.FailMode != w.FailMode ||
			g.Edited != w.Edited || g.Target != w.Target || g.ReplacedEvents != w.ReplacedEvents || g.DroppedEvents != w.DroppedEvents {
			t.Errorf("outcome %d = %+v, want %+v", i, g, w)
		}
	}
	if got[0].Detail == nil {
		t.Errorf("decision detail must be kept: %+v", got[0])
	}
}
