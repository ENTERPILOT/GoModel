package auditlog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

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
		for key, value := range entry.Data.RequestHeaders {
			headers[key] = value
		}
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
