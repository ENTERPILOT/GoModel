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
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/session"
)

// partialErrorReadCloser yields data once, then fails with err (a generic
// failure when unset).
type partialErrorReadCloser struct {
	data []byte
	err  error
	read bool
}

func (r *partialErrorReadCloser) failure() error {
	if r.err != nil {
		return r.err
	}
	return errors.New("injected request body failure")
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
		return 0, r.failure()
	}
	r.read = true
	n := copy(p, r.data)
	return n, r.failure()
}

func (r *partialErrorReadCloser) Close() error {
	return nil
}

func sessionTestContext(t *testing.T, path string, headers map[string]string) *echo.Context {
	t.Helper()
	opts := make([]echotest.Option, 0, len(headers))
	for name, value := range headers {
		opts = append(opts, echotest.WithHeader(name, value))
	}
	c, _ := echotest.Post(t, path, nil, opts...)
	req := c.Request()
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
	c, rec := echotest.Post(t, path, body)
	req := c.Request()
	req.ContentLength = contentLength
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
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, "11111111-2222-3333-4444-555555555555", got)
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
		require.Len(t, publisher.events, 1)
		got := publisher.events[0]
		require.Equal(t, auditlog.LiveEventAuditUpdated, got.eventType)
		require.Equal(t, "session-live", got.sessionID, "live event = %#v, want audit.updated with detected session", got)
		require.Equal(t, "session-live", entry.SessionID)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
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
		got := core.SessionIDFromContext(c.Request().Context())
		require.Equal(t, "auto-resolved-session", got)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, 1, lookup.calls)
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
		got := core.SessionIDFromContext(c.Request().Context())
		require.Equal(t, "parent-session", got)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
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
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	send("")
	require.Equal(t, 1, lookup.calls)

	authenticator.enabled = true
	send("managed")
	require.Equal(t, 1, lookup.calls)
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
				if tc.requestUserPath != "" {
					require.NotEmpty(t, got, "want scoped detector fallback")
					require.NotEqual(t, "parent-session", got, "want scoped detector fallback")
				} else {
					require.Equal(t, "detected-session", got, "want detector fallback")
				}
				return nil
			})
			err := handler(c)
			require.NoError(t, err)
			require.Equal(t, tc.wantCalls, lookup.calls)
		})
	}
}

func TestSessionCaptureSkipsNonModelPaths(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/health", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		id := core.SessionIDFromContext(c.Request().Context())
		require.Empty(t, id)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
}

func TestSessionCaptureNilDetectorIsNoOp(t *testing.T) {
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	called := false
	handler := sessionCapture(nil, nil, false)(func(c *echo.Context) error {
		called = true
		id := core.SessionIDFromContext(c.Request().Context())
		require.Empty(t, id)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.True(t, called)
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
		require.NoError(t, err)
		require.Len(t, remaining, len(body))

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, "big-body-session", got)
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
		require.NoError(t, err)
		require.Equal(t, body, string(remaining))

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, "chunked-session", got)
}

// JSON bodies past the audit capture limit still carry session signals: the
// handler decodes the whole body anyway, so detection reads it once and the
// handler reuses the same buffer. Only the audit capture stays bounded.
func TestSessionCaptureDetectsBodySignalPastAuditCaptureLimit(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	padding := strings.Repeat("x", int(auditlog.MaxBodyCapture))
	bodyText := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + padding + `"}],"session_id":"huge-body-session"}`
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(
		t,
		"/v1/chat/completions",
		body,
		int64(len(bodyText)),
		true,
	)

	var got string
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		require.Equal(t, int64(len(bodyText)), body.read, "session detection read = %d, want the complete body %d", body.read, len(bodyText))

		snapshot := core.GetRequestSnapshot(c.Request().Context())
		require.NotNil(t, snapshot)
		require.True(t, snapshot.BodyNotCaptured)
		require.Nil(t, snapshot.CapturedBodyView())

		first, err := requestBodyBytes(c)
		require.NoError(t, err)
		require.Equal(t, bodyText, string(first))

		second, err := requestBodyBytes(c)
		require.NoError(t, err)
		require.Equal(t, &second[0], &first[0])
		require.Equal(t, int64(len(bodyText)), body.read)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, "huge-body-session", got)
}

func TestSessionCaptureAutoDetectsChunkedBodyPastAuditCaptureLimit(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	padding := strings.Repeat("x", int(auditlog.MaxBodyCapture))
	bodyText := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"` + padding + `"}]}`
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(t, "/v1/chat/completions", body, -1, false)

	var got string
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		remaining, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		require.Equal(t, bodyText, string(remaining))

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got, "auto-"), "session id = %q, want content-derived id from an oversized chunked body", got)
}

// Opaque bodies are streamed upstream, so detection only peeks them: a
// known-oversized body is never read ahead of the handler and an
// unknown-length one is bounded and replayed intact.
func TestSessionCaptureDoesNotPreReadKnownOversizedOpaqueBody(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	bodyText := strings.Repeat("x", int(auditlog.MaxBodyCapture)+1)
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(
		t,
		"/p/openai/v1/chat/completions",
		body,
		int64(len(bodyText)),
		true,
	)

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		require.Equal(t, int64(0), body.read)

		remaining, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		require.Len(t, remaining, len(bodyText))

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
}

func TestSessionCaptureBoundsUnknownOversizedOpaqueBodyAndReplaysIt(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	bodyText := strings.Repeat("x", int(auditlog.MaxBodyCapture)+128)
	body := &countingReadCloser{reader: strings.NewReader(bodyText)}

	c, _ := sessionBodyTestContext(
		t,
		"/p/openai/v1/chat/completions",
		body,
		-1,
		false,
	)

	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		require.Equal(t, int64(auditlog.MaxBodyCapture+1), body.read)

		remaining, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		require.Equal(t, bodyText, string(remaining))

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
}

// The body size limit trips while detection materializes a chunked JSON body;
// the client must still see the limit's 413, not a generic read failure.
func TestSessionCaptureKeepsBodyLimitStatus(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	body := &partialErrorReadCloser{data: []byte(`{"model":"gpt-4o"`), err: echo.ErrStatusRequestEntityTooLarge}

	c, rec := sessionBodyTestContext(t, "/v1/chat/completions", body, -1, false)

	called := false
	handler := sessionCapture(detector, nil, false)(func(c *echo.Context) error {
		called = true
		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.False(t, called)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
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
	err := handler(c)
	require.NoError(t, err)
	require.False(t, called)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "failed to read request body")
}
