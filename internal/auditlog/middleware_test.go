package auditlog

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func TestApplyAuthenticationRefreshesLabelsFromContext(t *testing.T) {
	entry := &LogEntry{Data: &LogData{Labels: []string{"from-header"}}}
	ctx := core.WithRequestLabels(t.Context(), []string{"from-header", "from-key"})

	applyAuthentication(entry, ctx)

	if got, want := strings.Join(entry.Data.Labels, ","), "from-header,from-key"; got != want {
		t.Fatalf("entry.Data.Labels = %q, want %q", got, want)
	}
}

func TestApplyAuthenticationKeepsLabelsWhenContextHasNone(t *testing.T) {
	entry := &LogEntry{Data: &LogData{Labels: []string{"from-header"}}}

	applyAuthentication(entry, t.Context())

	if got, want := strings.Join(entry.Data.Labels, ","), "from-header"; got != want {
		t.Fatalf("entry.Data.Labels = %q, want %q", got, want)
	}
}

func TestEnrichEntryWithWorkflow_PrefersProviderNameForResolvedModel(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	entry := &LogEntry{ID: "provider-name-prefill"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{
				Provider: "openai",
				Model:    "gpt-5-nano",
			},
			ProviderName: "openai_test",
		},
	})

	if got := entry.Provider; got != "openai" {
		t.Fatalf("Provider = %q, want %q", got, "openai")
	}
	if got := entry.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
	if got := entry.ResolvedModel; got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
}

func TestEnrichEntryWithWorkflow_PreservesExecutedFailoverRoute(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// The handler already recorded the actual executed failover route and the
	// failover snapshot before the middleware re-applies the workflow.
	entry := &LogEntry{
		ID:            "failover-route",
		Provider:      "openai",
		ProviderName:  "openai",
		ResolvedModel: "openai/gpt-5.5",
		Data:          &LogData{Failover: &FailoverSnapshot{TargetModel: "openai/gpt-5.5"}},
	}
	c.Set(string(LogEntryKey), entry)

	// The workflow still carries only the planned primary resolution.
	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "anthropic",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-fable-5"},
			ProviderName:     "anthropic",
		},
	})

	if got := entry.ResolvedModel; got != "openai/gpt-5.5" {
		t.Fatalf("ResolvedModel = %q, want openai/gpt-5.5 (executed route must not be clobbered to primary)", got)
	}
	if got := entry.Provider; got != "openai" {
		t.Fatalf("Provider = %q, want openai", got)
	}
	if got := entry.ProviderName; got != "openai" {
		t.Fatalf("ProviderName = %q, want openai", got)
	}
}

func TestEnrichEntryWithWorkflow_FailoverSnapshotDoesNotSuppressMissingRouteFields(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// A failover snapshot exists, but the executed route only populated the
	// resolved model — provider/provider_name came back empty. The snapshot
	// alone must not suppress the remaining fields; the workflow's planned
	// values should fill the gaps instead of leaving them blank.
	entry := &LogEntry{
		ID:            "failover-partial-route",
		ResolvedModel: "openai/gpt-5.5",
		Data:          &LogData{Failover: &FailoverSnapshot{TargetModel: "openai/gpt-5.5"}},
	}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "anthropic",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-fable-5"},
			ProviderName:     "anthropic",
		},
	})

	if got := entry.ResolvedModel; got != "openai/gpt-5.5" {
		t.Fatalf("ResolvedModel = %q, want openai/gpt-5.5 (recorded route must win)", got)
	}
	if got := entry.Provider; got != "anthropic" {
		t.Fatalf("Provider = %q, want anthropic (workflow fills the empty field)", got)
	}
	if got := entry.ProviderName; got != "anthropic" {
		t.Fatalf("ProviderName = %q, want anthropic (workflow fills the empty field)", got)
	}
}

func TestMiddlewarePublishesStartedEventWithRedactedRequestHeaders(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:    true,
			LogHeaders: true,
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "req-started")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Test", "visible")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		if len(logger.events) != 1 {
			t.Fatalf("live events before handler = %d, want 1", len(logger.events))
		}
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(logger.events) == 0 {
		t.Fatal("no live events were published")
	}

	started := logger.events[0]
	if started.eventType != LiveEventAuditStarted {
		t.Fatalf("first event type = %q, want %q", started.eventType, LiveEventAuditStarted)
	}
	if got := started.requestHeaders["Authorization"]; got != "[REDACTED]" {
		t.Fatalf("Authorization header = %q, want [REDACTED]", got)
	}
	if got := started.requestHeaders["X-Test"]; got != "visible" {
		t.Fatalf("X-Test header = %q, want visible", got)
	}
}

func TestMiddlewarePublishesWorkflowUpdateWithCapturedRequestBody(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(`{"model":"from-stream"}`)}
	req.Body = trackedBody
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"from-snapshot"}`),
		false,
		"req-body",
		nil,
	)))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithWorkflow(c, &core.Workflow{
			Policy: &core.ResolvedWorkflowPolicy{
				VersionID: "audit-enabled",
				Features: core.WorkflowFeatures{
					Audit: true,
				},
			},
		})
		if len(logger.events) != 2 {
			t.Fatalf("live events before handler completes = %d, want 2", len(logger.events))
		}
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if trackedBody.readCalls != 0 {
		t.Fatalf("request body was read %d times, want 0", trackedBody.readCalls)
	}
	updated := logger.events[1]
	if updated.eventType != LiveEventAuditUpdated {
		t.Fatalf("second event type = %q, want %q", updated.eventType, LiveEventAuditUpdated)
	}
	body, ok := updated.requestBody.(map[string]any)
	if !ok {
		t.Fatalf("request body = %T, want map[string]any", updated.requestBody)
	}
	if got := body["model"]; got != "from-snapshot" {
		t.Fatalf("request body model = %#v, want from-snapshot", got)
	}
}

func TestMiddlewareDoesNotPublishRequestBodyForAuditDisabledWorkflow(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"hidden"}`),
		false,
		"req-hidden",
		nil,
	)))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		workflow := &core.Workflow{
			Policy: &core.ResolvedWorkflowPolicy{
				VersionID: "audit-disabled",
				Features: core.WorkflowFeatures{
					Audit: false,
				},
			},
		}
		EnrichEntryWithWorkflow(c, workflow)
		if got := core.GetWorkflow(c.Request().Context()); got != workflow {
			t.Fatal("request context workflow was not synchronized")
		}
		if len(logger.events) != 2 {
			t.Fatalf("live events before handler completes = %d, want 2", len(logger.events))
		}
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(logger.events) != 3 {
		t.Fatalf("live events after handler completes = %d, want 3", len(logger.events))
	}
	if body := logger.events[1].requestBody; body != nil {
		t.Fatalf("audit-disabled workflow request body = %#v, want nil", body)
	}
	if removed := logger.events[2]; removed.eventType != LiveEventAuditRemoved {
		t.Fatalf("third event type = %q, want %q", removed.eventType, LiveEventAuditRemoved)
	} else if removed.requestBody != nil {
		t.Fatalf("audit removed request body = %#v, want nil", removed.requestBody)
	}
	if logger.writes != 0 {
		t.Fatalf("audit writes = %d, want 0", logger.writes)
	}
}

type capturedLiveEvent struct {
	eventType      string
	requestHeaders map[string]string
	requestBody    any
}

type captureLiveLogger struct {
	cfg    Config
	events []capturedLiveEvent
	writes int
}

func (l *captureLiveLogger) Write(_ *LogEntry) {
	l.writes++
}

func (l *captureLiveLogger) Config() Config {
	return l.cfg
}

func (l *captureLiveLogger) Close() error {
	return nil
}

func (l *captureLiveLogger) PublishLiveEvent(eventType string, entry *LogEntry) {
	headers := map[string]string(nil)
	if entry != nil && entry.Data != nil && entry.Data.RequestHeaders != nil {
		headers = make(map[string]string, len(entry.Data.RequestHeaders))
		maps.Copy(headers, entry.Data.RequestHeaders)
	}
	var requestBody any
	if entry != nil && entry.Data != nil {
		requestBody = entry.Data.RequestBody
	}
	l.events = append(l.events, capturedLiveEvent{
		eventType:      eventType,
		requestHeaders: headers,
		requestBody:    requestBody,
	})
}

func TestMiddlewarePublishesRemovalWhenHandlerPanics(t *testing.T) {
	logger := &captureLiveLogger{cfg: Config{Enabled: true}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "req-panic")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		panic("handler exploded")
	})

	// The panic must keep propagating to the outer recover middleware; the
	// audit middleware only publishes the terminal live event on the way out.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("panic was swallowed by the audit middleware")
			}
		}()
		_ = handler(c)
	}()

	if len(logger.events) != 2 {
		t.Fatalf("live events = %d, want started + removed", len(logger.events))
	}
	if logger.events[0].eventType != LiveEventAuditStarted {
		t.Fatalf("first event = %q, want %q", logger.events[0].eventType, LiveEventAuditStarted)
	}
	if logger.events[1].eventType != LiveEventAuditRemoved {
		t.Fatalf("second event = %q, want %q (terminal event that evicts the live snapshot)", logger.events[1].eventType, LiveEventAuditRemoved)
	}
}
