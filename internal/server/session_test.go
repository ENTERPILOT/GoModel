package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

type partialErrorReadCloser struct {
	data []byte
	read bool
}

type sessionLiveEvent struct {
	eventType string
	sessionID string
}

type sessionLivePublisher struct {
	events []sessionLiveEvent
}

type sessionParentLookup struct {
	auditlog.Reader
	entry *auditlog.InteractionParent
	err   error
	calls int
}

func (l *sessionParentLookup) GetInteractionParent(_ context.Context, _ string) (*auditlog.InteractionParent, error) {
	l.calls++
	return l.entry, l.err
}

func (p *sessionLivePublisher) PublishLiveEvent(eventType string, entry *auditlog.LogEntry) {
	p.events = append(p.events, sessionLiveEvent{eventType: eventType, sessionID: entry.SessionID})
}

func (r *partialErrorReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("injected request body failure")
	}
	r.read = true
	n := copy(p, r.data)
	return n, errors.New("injected request body failure")
}

func (r *partialErrorReadCloser) Close() error {
	return nil
}

func sessionTestContext(t *testing.T, path string, headers map[string]string) *echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	snapshot := core.NewRequestSnapshot(
		http.MethodPost, path, nil, nil, req.Header, "application/json", nil, false, "req-1", nil,
	)
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), snapshot)))
	return c
}

func sessionBodyTestContext(
	t *testing.T,
	path string,
	body io.ReadCloser,
	contentLength int64,
	bodyNotCaptured bool,
) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Body = body
	req.ContentLength = contentLength
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	snapshot := core.NewRequestSnapshot(
		http.MethodPost, path, nil, nil, req.Header,
		"application/json", nil, bodyNotCaptured, "req-1", nil,
	)
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), snapshot)))
	return c, rec
}

func TestSessionCaptureStampsContext(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	var got string
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("session id = %q, want header value", got)
	}
}

func TestSessionCapturePublishesSessionBeforeDownstreamHandler(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "session-live",
	})
	entry := &auditlog.LogEntry{ID: "audit-live"}
	publisher := &sessionLivePublisher{}
	c.Set(string(auditlog.LogEntryKey), entry)
	c.Set(string(auditlog.LogEntryLivePublisherKey), publisher)

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		if len(publisher.events) != 1 {
			t.Fatalf("live events before handler = %d, want 1", len(publisher.events))
		}
		if got := publisher.events[0]; got.eventType != auditlog.LiveEventAuditUpdated || got.sessionID != "session-live" {
			t.Fatalf("live event = %#v, want audit.updated with detected session", got)
		}
		if entry.SessionID != "session-live" {
			t.Fatalf("entry session = %q, want detected session", entry.SessionID)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureInheritsTrustedInteractionParent(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/v1/responses", map[string]string{
		interactionParentHeader: "parent-log",
		"X-Session-Id":          "raw-session-that-must-not-win",
	})
	setInteractionContinuationAllowed(c, true)
	lookup := &sessionParentLookup{entry: &auditlog.InteractionParent{
		SessionID: "auto-resolved-session", UserPath: "/",
	}}

	handler := sessionCapture(detector, lookup, false)(func(c *echo.Context) error {
		if got := core.SessionIDFromContext(c.Request().Context()); got != "auto-resolved-session" {
			t.Fatalf("session id = %q, want inherited parent session", got)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if lookup.calls != 1 {
		t.Fatalf("parent lookups = %d, want 1", lookup.calls)
	}
}

func TestSessionCaptureAllowsParentWhenAuthenticationIsDisabled(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/v1/responses", map[string]string{
		interactionParentHeader: "parent-log",
	})
	lookup := &sessionParentLookup{entry: &auditlog.InteractionParent{
		SessionID: "parent-session", UserPath: "/",
	}}

	handler := sessionCapture(detector, lookup, true)(func(c *echo.Context) error {
		if got := core.SessionIDFromContext(c.Request().Context()); got != "parent-session" {
			t.Fatalf("session id = %q, want inherited parent session", got)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureUsesLiveAuthenticationDecision(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	lookup := &sessionParentLookup{entry: &auditlog.InteractionParent{
		SessionID: "parent-session", UserPath: "/",
	}}
	authenticator := &mockAuthenticator{
		tokenToID: map[string]string{"managed": "key-1"},
	}
	provider := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes:   map[string]string{"gpt-4o": "openai"},
		response: &core.ChatResponse{
			ID: "chatcmpl-test", Object: "chat.completion", Model: "gpt-4o",
			Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
		},
	}
	srv := New(provider, &Config{
		Authenticator:   authenticator,
		SessionDetector: detector,
		AuditReader:     lookup,
	})
	send := func(token string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(interactionParentHeader, "parent-log")
		req.Header.Set("X-Session-Id", "detected-session")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	}

	send("")
	if lookup.calls != 1 {
		t.Fatalf("no-auth parent lookups = %d, want 1", lookup.calls)
	}

	authenticator.enabled = true
	send("managed")
	if lookup.calls != 1 {
		t.Fatalf("non-dashboard managed key performed a parent lookup; calls = %d", lookup.calls)
	}
}

func TestSessionCaptureRejectsUntrustedOrCrossPathParent(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	for _, tc := range []struct {
		name            string
		trusted         bool
		parentID        string
		parent          *auditlog.InteractionParent
		lookupErr       error
		requestUserPath string
		wantCalls       int
	}{
		{name: "untrusted", parentID: "parent-log", parent: &auditlog.InteractionParent{SessionID: "parent-session"}},
		{name: "comma in parent id", trusted: true, parentID: "parent,log", wantCalls: 0},
		{name: "oversized parent id", trusted: true, parentID: strings.Repeat("x", 201), wantCalls: 0},
		{name: "lookup error", trusted: true, parentID: "parent-log", lookupErr: errors.New("lookup failed"), wantCalls: 1},
		{name: "missing parent", trusted: true, parentID: "parent-log", wantCalls: 1},
		{name: "blank parent session", trusted: true, parentID: "parent-log", parent: &auditlog.InteractionParent{}, wantCalls: 1},
		{name: "different user path", trusted: true, parentID: "parent-log", parent: &auditlog.InteractionParent{SessionID: "parent-session", UserPath: "/team"}, wantCalls: 1},
		{name: "legacy root parent from user path", trusted: true, parentID: "parent-log", parent: &auditlog.InteractionParent{SessionID: "parent-session"}, requestUserPath: "/team", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
				interactionParentHeader: tc.parentID,
				"X-Session-Id":          "detected-session",
			})
			if tc.requestUserPath != "" {
				req := c.Request()
				c.SetRequest(req.WithContext(core.WithEffectiveUserPath(req.Context(), tc.requestUserPath)))
			}
			setInteractionContinuationAllowed(c, tc.trusted)
			lookup := &sessionParentLookup{entry: tc.parent, err: tc.lookupErr}

			handler := sessionCapture(detector, lookup, false)(func(c *echo.Context) error {
				got := core.SessionIDFromContext(c.Request().Context())
				if tc.requestUserPath != "" && (got == "" || got == "parent-session") {
					t.Fatalf("session id = %q, want scoped detector fallback", got)
				}
				if tc.requestUserPath == "" && got != "detected-session" {
					t.Fatalf("session id = %q, want detector fallback", got)
				}
				return nil
			})
			if err := handler(c); err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if lookup.calls != tc.wantCalls {
				t.Fatalf("parent lookups = %d, want %d", lookup.calls, tc.wantCalls)
			}
		})
	}
}

func TestSessionCaptureSkipsNonModelPaths(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/health", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		if id := core.SessionIDFromContext(c.Request().Context()); id != "" {
			t.Fatalf("session id = %q, want empty on non-model path", id)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureNilDetectorIsNoOp(t *testing.T) {
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	called := false
	handler := sessionCapture(nil, nil, false)(func(c *echo.Context) error {
		called = true
		if id := core.SessionIDFromContext(c.Request().Context()); id != "" {
			t.Fatalf("session id = %q, want empty with nil detector", id)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !called {
		t.Fatal("next handler not called")
	}
}

// Bodies over the 64 KiB ingress capture limit (or chunked requests) are not
// on the snapshot when SessionCapture runs; chat/responses requests must
// materialize the body so body signals and content detection still work.
func TestSessionCaptureMaterializesLargeBodies(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)

	padding := strings.Repeat("x", 80*1024)
	body := `{"model":"gpt-4o","session_id":"big-body-session","messages":[{"role":"user","content":"` + padding + `"}]}`

	// Ingress declined the body (over the inline capture limit).
	c, _ := sessionBodyTestContext(
		t,
		"/v1/chat/completions",
		io.NopCloser(strings.NewReader(body)),
		int64(len(body)),
		true,
	)

	var got string
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		// The handler must still be able to read the full body afterwards.
		remaining, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatalf("body read after capture: %v", err)
		}
		if len(remaining) != len(body) {
			t.Fatalf("body truncated after capture: %d != %d", len(remaining), len(body))
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "big-body-session" {
		t.Fatalf("session id = %q, want body signal from a large body", got)
	}
}

func TestSessionCaptureMaterializesChunkedBody(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	body := `{"model":"gpt-4o","session_id":"chunked-session","messages":[{"role":"user","content":"hi"}]}`

	c, _ := sessionBodyTestContext(
		t,
		"/v1/chat/completions",
		io.NopCloser(strings.NewReader(body)),
		-1,
		false,
	)

	var got string
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		remaining, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatalf("body read after capture: %v", err)
		}
		if string(remaining) != body {
			t.Fatalf("replayed body = %q, want original", remaining)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "chunked-session" {
		t.Fatalf("session id = %q, want chunked body signal", got)
	}
}

func TestSessionCaptureDoesNotPreReadKnownOversizedBody(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	bodyText := strings.Repeat("x", int(auditlog.MaxBodyCapture)+1)
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(
		t,
		"/v1/chat/completions",
		body,
		int64(len(bodyText)),
		true,
	)

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		if body.read != 0 {
			t.Fatalf("oversized body read before handler: %d bytes", body.read)
		}
		remaining, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatalf("handler body read: %v", err)
		}
		if len(remaining) != len(bodyText) {
			t.Fatalf("handler body length = %d, want %d", len(remaining), len(bodyText))
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureBoundsUnknownOversizedBodyAndReplaysIt(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	bodyText := strings.Repeat("x", int(auditlog.MaxBodyCapture)+128)
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(
		t,
		"/v1/chat/completions",
		body,
		-1,
		false,
	)

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		if body.read != auditlog.MaxBodyCapture+1 {
			t.Fatalf("session detection read = %d, want bounded %d", body.read, auditlog.MaxBodyCapture+1)
		}
		remaining, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatalf("handler body read: %v", err)
		}
		if string(remaining) != bodyText {
			t.Fatal("bounded session peek did not replay the complete body")
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureRejectsBodyReadFailure(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	body := &partialErrorReadCloser{data: []byte(`{"model":"gpt-4o"}`)}

	c, rec := sessionBodyTestContext(t, "/v1/chat/completions", body, -1, false)

	called := false
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		called = true
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if called {
		t.Fatal("downstream handler called after request body read failure")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "failed to read request body") {
		t.Fatalf("response = %q, want body read failure", rec.Body.String())
	}
}
