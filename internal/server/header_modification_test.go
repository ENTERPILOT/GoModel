package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

type stubHeaderPolicy struct {
	name     string
	mutation *core.HeaderPlan
	seen     http.Header
}

func (m *stubHeaderPolicy) Name() string { return m.name }

func (m *stubHeaderPolicy) ResolveHeaderPlan(input core.HeaderPolicyInput) *core.HeaderPlan {
	m.seen = input.Headers
	return m.mutation
}

type stubHeaderPolicyResolver struct {
	mutators []core.HeaderPolicy
}

func (r *stubHeaderPolicyResolver) HeaderPoliciesForContext(context.Context) []core.HeaderPolicy {
	return r.mutators
}

func runHeaderModificationMiddleware(t *testing.T, resolver HeaderPolicyResolver, auditLogger auditlog.LoggerInterface, entry *auditlog.LogEntry) (*core.HeaderPlan, *auditlog.LogEntry) {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "cline/3.2")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if entry != nil {
		c.Set(string(auditlog.LogEntryKey), entry)
	}

	var carried *core.HeaderPlan
	handler := HeaderPolicyPlanningMiddleware(resolver, auditLogger)(func(c *echo.Context) error {
		carried = core.HeaderPlanFromContext(c.Request().Context())
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("middleware failed: %v", err)
	}
	return carried, entry
}

func TestHeaderModificationMiddlewareMergesAndRecords(t *testing.T) {
	first := &stubHeaderPolicy{
		name:     "pin-beta",
		mutation: &core.HeaderPlan{Set: map[string]string{"Anthropic-Beta": "context-1m"}},
	}
	skipped := &stubHeaderPolicy{name: "no-match"}
	second := &stubHeaderPolicy{
		name:     "strip-debug",
		mutation: &core.HeaderPlan{Remove: []string{"X-Debug"}},
	}
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{first, skipped, second}}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogHeaders: true}}
	entry := &auditlog.LogEntry{}

	carried, entry := runHeaderModificationMiddleware(t, resolver, auditLogger, entry)

	if carried == nil {
		t.Fatal("expected merged mutation in context")
	}
	if carried.Set["Anthropic-Beta"] != "context-1m" || len(carried.Remove) != 1 || carried.Remove[0] != "X-Debug" {
		t.Fatalf("unexpected merged mutation: %+v", carried)
	}
	if first.seen.Get("User-Agent") != "cline/3.2" {
		t.Fatal("mutators must receive the inbound headers")
	}

	revisions := entry.Data.RequestRevisions
	if len(revisions) != 2 {
		t.Fatalf("expected 2 revisions (matched steps only), got %d", len(revisions))
	}
	if revisions[0].Rewriter != "pin-beta" || revisions[1].Rewriter != "strip-debug" {
		t.Fatalf("unexpected revision names: %+v", revisions)
	}
	if revisions[0].Seq != 1 || revisions[1].Seq != 2 {
		t.Fatalf("unexpected revision sequence: %+v", revisions)
	}
	if revisions[0].Headers == nil {
		t.Fatal("first revision must record a header delta")
	}
	setValues, ok := revisions[0].Headers.Set.(map[string]string)
	if !ok || setValues["Anthropic-Beta"] != "context-1m" {
		t.Fatalf("first revision must record the set delta: %+v", revisions[0].Headers)
	}
	if revisions[0].Body != nil {
		t.Fatal("header revisions must not carry a body")
	}
	if revisions[1].Headers == nil || len(revisions[1].Headers.Removed) != 1 || revisions[1].Headers.Removed[0] != "X-Debug" {
		t.Fatalf("second revision must record the removed delta: %+v", revisions[1].Headers)
	}
}

func TestHeaderModificationMiddlewareRecordsNamesOnlyWhenHeaderLoggingDisabled(t *testing.T) {
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{
		&stubHeaderPolicy{
			name: "inject-secret",
			mutation: &core.HeaderPlan{
				Set:    map[string]string{"X-Custom-Auth": "literal-secret", "Anthropic-Beta": "context-1m"},
				Remove: []string{"X-Internal-Debug"},
			},
		},
	}}
	entry := &auditlog.LogEntry{}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogHeaders: false}}

	_, entry = runHeaderModificationMiddleware(t, resolver, auditLogger, entry)

	revision := entry.Data.RequestRevisions[0]
	names, ok := revision.Headers.Set.([]string)
	if !ok {
		t.Fatalf("set delta type = %T, want []string", revision.Headers.Set)
	}
	if len(names) != 2 || names[0] != "Anthropic-Beta" || names[1] != "X-Custom-Auth" {
		t.Fatalf("set names = %v, want sorted names only", names)
	}
	if len(revision.Headers.Removed) != 1 || revision.Headers.Removed[0] != "X-Internal-Debug" {
		t.Fatalf("removed names = %v", revision.Headers.Removed)
	}
	encoded, err := json.Marshal(revision.Headers)
	if err != nil {
		t.Fatalf("marshal name-only delta: %v", err)
	}
	if got, want := string(encoded), `{"set":["Anthropic-Beta","X-Custom-Auth"],"removed":["X-Internal-Debug"]}`; got != want {
		t.Fatalf("encoded name-only delta = %s, want %s", got, want)
	}
}

func TestHeaderModificationMiddlewareRedactsValuesWhenHeaderLoggingEnabled(t *testing.T) {
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{
		&stubHeaderPolicy{
			name: "defense-in-depth",
			mutation: &core.HeaderPlan{Set: map[string]string{
				"X-Api-Key":       "secret",
				"X-Feature":       "visible",
				"X-Session-Token": "also-secret",
			}},
		},
	}}
	entry := &auditlog.LogEntry{}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogHeaders: true}}

	_, entry = runHeaderModificationMiddleware(t, resolver, auditLogger, entry)

	values, ok := entry.Data.RequestRevisions[0].Headers.Set.(map[string]string)
	if !ok {
		t.Fatalf("set delta type = %T, want map[string]string", entry.Data.RequestRevisions[0].Headers.Set)
	}
	if values["X-Api-Key"] != "[REDACTED]" || values["X-Session-Token"] != "[REDACTED]" || values["X-Feature"] != "visible" {
		t.Fatalf("unexpected redacted values: %v", values)
	}
}

func TestHeaderModificationMiddlewareRedactsValueCopiedFromSensitiveSource(t *testing.T) {
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{
		&stubHeaderPolicy{
			name: "copy-session",
			mutation: &core.HeaderPlan{
				Set:          map[string]string{"X-Team": "secret-session-token"},
				SensitiveSet: []string{"X-Team"},
			},
		},
	}}
	entry := &auditlog.LogEntry{}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogHeaders: true}}
	_, entry = runHeaderModificationMiddleware(t, resolver, auditLogger, entry)
	values := entry.Data.RequestRevisions[0].Headers.Set.(map[string]string)
	if values["X-Team"] != auditlog.RedactedHeaderValue {
		t.Fatalf("copied sensitive value was recorded: %v", values)
	}
}

func TestHeaderModificationMiddlewareNoMatchLeavesContextUntouched(t *testing.T) {
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{&stubHeaderPolicy{name: "no-match"}}}
	entry := &auditlog.LogEntry{}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true}}

	carried, entry := runHeaderModificationMiddleware(t, resolver, auditLogger, entry)

	if carried != nil {
		t.Fatalf("expected no mutation in context, got %+v", carried)
	}
	if entry.Data != nil && len(entry.Data.RequestRevisions) != 0 {
		t.Fatalf("expected no revisions, got %+v", entry.Data.RequestRevisions)
	}
}

func TestHeaderModificationMiddlewareAuditDisabled(t *testing.T) {
	resolver := &stubHeaderPolicyResolver{mutators: []core.HeaderPolicy{
		&stubHeaderPolicy{name: "pin", mutation: &core.HeaderPlan{Set: map[string]string{"X-A": "1"}}},
	}}
	entry := &auditlog.LogEntry{}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: false}}

	carried, entry := runHeaderModificationMiddleware(t, resolver, auditLogger, entry)

	if carried == nil || carried.Set["X-A"] != "1" {
		t.Fatal("mutation must still apply when audit is disabled")
	}
	if entry.Data != nil && len(entry.Data.RequestRevisions) != 0 {
		t.Fatal("no revisions should be recorded when audit is disabled")
	}
}

func TestBuildPassthroughHeadersAppliesMutation(t *testing.T) {
	src := http.Header{
		"X-Debug":       []string{"1"},
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer secret"},
	}
	mutation := &core.HeaderPlan{
		Set:    map[string]string{"Anthropic-Beta": "context-1m"},
		Remove: []string{"X-Debug"},
	}
	ctx := core.WithHeaderPlan(context.Background(), mutation)

	dst := buildPassthroughHeaders(ctx, src)

	if dst.Get("Anthropic-Beta") != "context-1m" {
		t.Fatalf("set not applied: %v", dst)
	}
	if _, exists := dst["X-Debug"]; exists {
		t.Fatal("remove not applied")
	}
	if dst.Get("Accept") != "application/json" {
		t.Fatal("unrelated forwarded header lost")
	}
	if dst.Get("Authorization") != "" {
		t.Fatal("credentials must never be forwarded")
	}
}
