package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

func TestMCPAuditLabel(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "tools/call labels with the tool name",
			body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github_create_issue","arguments":{}}}`,
			want: "github_create_issue",
		},
		{
			name: "prompts/get labels with the prompt name",
			body: `{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"github_triage"}}`,
			want: "github_triage",
		},
		{
			name: "other methods label with the method",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			want: "tools/list",
		},
		{
			name: "initialize labels with the method",
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
			want: "initialize",
		},
		{
			name: "notification without params.name keeps the method",
			body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			want: "notifications/initialized",
		},
		{
			name: "bare response is unlabelable",
			body: `{"jsonrpc":"2.0","id":9,"result":{}}`,
			want: "",
		},
		{
			name: "malformed frame is unlabelable",
			body: `not json`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcpAuditLabel([]byte(tt.body))
			require.Equal(t, tt.want, got, "mcpAuditLabel(%s) = %q, want %q", tt.body, got, tt.want)
		})
	}
}

// rejectingRateLimiter breaches every acquisition with a requests-window rule.
type rejectingRateLimiter struct{}

func (rejectingRateLimiter) Acquire(ratelimit.Subjects, time.Time) (*ratelimit.Reservation, error) {
	limit := int64(1)
	return nil, &ratelimit.ExceededError{
		Rule:       ratelimit.Rule{MaxRequests: &limit, PeriodSeconds: 60},
		Scope:      ratelimit.ScopeRequests,
		Observed:   2,
		Limit:      1,
		RetryAfter: time.Minute,
	}
}

func (rejectingRateLimiter) RouteAvailable(string, string) bool { return true }

// rejectingBudgetChecker refuses every request.
type rejectingBudgetChecker struct{}

func (rejectingBudgetChecker) Check(context.Context, budget.Subjects, time.Time) error {
	return context.DeadlineExceeded
}

func newEmptyMCPGateway(t *testing.T) *mcpgateway.Service {
	t.Helper()
	gateway, err := mcpgateway.NewService(context.Background(), mcpgateway.Options{})
	require.NoError(t, err)

	t.Cleanup(gateway.Close)
	return gateway
}

const mcpInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func newMCPTestContext(t *testing.T, method, body string) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var reader any
	if body != "" {
		reader = body
	}
	return echotest.Request(t, method, "/mcp", reader,
		echotest.WithHeader("Content-Type", "application/json"),
		echotest.WithHeader("Accept", "application/json, text/event-stream"))
}

func errorCodeFromBody(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	err := json.Unmarshal(body, &envelope)
	require.NoError(t, err)

	return envelope.Error.Code
}

// TestMCPServiceHandleGates covers the /mcp admission path: the feature gate,
// rate-limit and budget enforcement on POSTs, and delegation to the gateway.
func TestMCPServiceHandleGates(t *testing.T) {
	t.Run("disabled gateway is 501", func(t *testing.T) {
		svc := &mcpService{enabled: false}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusNotImplemented, rec.Code)
	})

	t.Run("nil gateway is 501 even when enabled", func(t *testing.T) {
		svc := &mcpService{enabled: true}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusNotImplemented, rec.Code)
	})

	t.Run("rate limit breach is 429 and never reaches the gateway", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, rateLimiter: rejectingRateLimiter{}}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
		code := errorCodeFromBody(t, rec.Body.Bytes())
		require.Equal(t, "rate_limit_exceeded", code)
		require.NotEmpty(t, rec.Header().Get("Retry-After"))
		require.Empty(t, rec.Header().Get("Mcp-Session-Id"))
	})

	t.Run("budget rejection blocks after rate limiting", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, budgetChecker: rejectingBudgetChecker{}}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		code := errorCodeFromBody(t, rec.Body.Bytes())
		require.Equal(t, "budget_check_failed", code)
		require.Empty(t, rec.Header().Get("Mcp-Session-Id"))
	})

	t.Run("happy path delegates to the gateway", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotEmpty(t, rec.Header().Get("Mcp-Session-Id"))
	})

	t.Run("body logging captures the JSON-RPC exchange on the audit entry", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, logBodies: true}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		entry := &auditlog.LogEntry{}
		c.Set(string(auditlog.LogEntryKey), entry)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, entry.Data)
		require.NotNil(t, entry.Data.RequestBody)
		require.NotNil(t, entry.Data.ResponseBody, "response body missing from the audit entry (content-type %q, body %q)", rec.Header().Get("Content-Type"), rec.Body.String())

		frame, ok := entry.Data.ResponseBody.(map[string]any)
		require.True(t, ok, "response body type = %T, want decoded JSON-RPC frame", entry.Data.ResponseBody)
		require.Equal(t, "2.0", frame["jsonrpc"], "response frame = %v, want a JSON-RPC message", frame)
	})

	t.Run("body logging off leaves the audit entry without bodies", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, logBodies: false}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		entry := &auditlog.LogEntry{}
		c.Set(string(auditlog.LogEntryKey), entry)
		err := svc.handle(c, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		if entry.Data != nil {
			require.Nil(t, entry.Data.RequestBody, "body logging is off")
			require.Nil(t, entry.Data.ResponseBody, "body logging is off")
		}
	})

	t.Run("GET skips the admission gates", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, rateLimiter: rejectingRateLimiter{}}
		c, rec := newMCPTestContext(t, http.MethodGet, "")
		err := svc.handle(c, "")
		require.NoError(t, err)

		// The SDK rejects the sessionless GET itself; the point is that the
		// breaching rate limiter never turned it into a 429.
		require.NotEqual(t, http.StatusTooManyRequests, rec.Code)
	})

	t.Run("unknown pinned server is 404", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true}
		c, rec := newMCPTestContext(t, http.MethodPost, mcpInitializeBody)
		err := svc.handle(c, "ghost")
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

// TestMCPResponseCaptureWrite covers the audit-cap boundaries of the response
// tee: the buffer stops at auditlog.MaxBodyCapture (flagging truncation) while
// the client always receives every byte.
func TestMCPResponseCaptureWrite(t *testing.T) {
	tests := []struct {
		name          string
		writes        []string
		wantCaptured  string
		wantTruncated bool
	}{
		{
			name:         "under the cap captures everything",
			writes:       []string{"data: {}", "\n\n"},
			wantCaptured: "data: {}\n\n",
		},
		{
			name:         "exactly the cap captures everything untruncated",
			writes:       []string{strings.Repeat("x", auditlog.MaxBodyCapture)},
			wantCaptured: strings.Repeat("x", auditlog.MaxBodyCapture),
		},
		{
			name:          "overflowing write is cut at the cap and flagged",
			writes:        []string{strings.Repeat("x", auditlog.MaxBodyCapture+1)},
			wantCaptured:  strings.Repeat("x", auditlog.MaxBodyCapture),
			wantTruncated: true,
		},
		{
			name:          "write after a full buffer only flags truncation",
			writes:        []string{strings.Repeat("x", auditlog.MaxBodyCapture), "overflow"},
			wantCaptured:  strings.Repeat("x", auditlog.MaxBodyCapture),
			wantTruncated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &mcpResponseCapture{ResponseWriter: rec}
			var forwarded int
			for _, w := range tt.writes {
				n, err := capture.Write([]byte(w))
				require.NoError(t, err)

				forwarded += n
			}
			got := capture.body.String()
			require.Equal(t, tt.wantCaptured, got)
			require.Equal(t, tt.wantTruncated, capture.truncated)
			want := len(strings.Join(tt.writes, ""))
			require.Equal(t, want, rec.Body.Len())
			require.Equal(t, want, forwarded)
		})
	}
}

// TestMCPResponseCaptureEnrich covers what the tee records on the audit entry:
// only SSE replies (the middleware owns the rest), with the truncation flag
// carried through.
func TestMCPResponseCaptureEnrich(t *testing.T) {
	tests := []struct {
		name          string
		contentType   string
		body          string
		truncated     bool
		wantBody      bool
		wantTruncated bool
	}{
		{
			name:        "SSE reply is recorded",
			contentType: "text/event-stream; charset=utf-8",
			body:        "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
			wantBody:    true,
		},
		{
			name:          "truncated SSE reply sets the overflow flag",
			contentType:   "text/event-stream",
			body:          "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"resu",
			truncated:     true,
			wantBody:      true,
			wantTruncated: true,
		},
		{
			name:        "non-SSE reply is left to the middleware capture",
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"result":{}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newMCPTestContext(t, http.MethodPost, "")
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)
			capture := &mcpResponseCapture{ResponseWriter: rec}
			capture.Header().Set("Content-Type", tt.contentType)
			_, err := capture.Write([]byte(tt.body))
			require.NoError(t, err)

			capture.truncated = tt.truncated

			capture.enrich(c)

			gotBody := entry.Data != nil && entry.Data.ResponseBody != nil
			require.Equal(t, tt.wantBody, gotBody)

			gotTruncated := entry.Data != nil && entry.Data.ResponseBodyTooBigToHandle
			require.Equal(t, tt.wantTruncated, gotTruncated)
		})
	}
}

// configLogger stubs auditlog.LoggerInterface with a fixed config.
type configLogger struct{ cfg auditlog.Config }

func (l configLogger) Write(*auditlog.LogEntry) {}
func (l configLogger) Config() auditlog.Config  { return l.cfg }
func (l configLogger) Close() error             { return nil }

// TestHandlerMCPLogBodies covers how Handler.mcp() derives the service's
// logBodies flag from the audit logger configuration.
func TestHandlerMCPLogBodies(t *testing.T) {
	tests := []struct {
		name   string
		logger auditlog.LoggerInterface
		want   bool
	}{
		{name: "nil logger defaults to off", logger: nil, want: false},
		{name: "body logging disabled stays off", logger: configLogger{cfg: auditlog.Config{Enabled: true}}, want: false},
		{name: "body logging enabled propagates", logger: configLogger{cfg: auditlog.Config{Enabled: true, LogBodies: true}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{logger: tt.logger}
			got := h.mcp().logBodies
			require.Equal(t, tt.want, got)
		})
	}
}

func TestEnrichMCPAuditEntryRestoresBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	c, _ := echotest.Post(t, "/mcp", body)

	enrichMCPAuditEntry(c, false)

	restored, err := io.ReadAll(c.Request().Body)
	require.NoError(t, err)
	require.Equal(t, body, string(restored))
}

func TestEnrichMCPAuditEntryCapturesRequestBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"linear_list_issues"}}`
	entry := &auditlog.LogEntry{}
	c, _ := echotest.Post(t, "/mcp", body, echotest.WithValue(string(auditlog.LogEntryKey), entry))

	enrichMCPAuditEntry(c, true)

	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.RequestBody)

	captured, ok := auditlog.BodyDocument(entry.Data.RequestBody).(map[string]any)
	require.True(t, ok, "captured body type = %T, want JSON object", entry.Data.RequestBody)
	require.Equal(t, "tools/call", captured["method"])
	require.Equal(t, "linear_list_issues", entry.RequestedModel)
	require.Equal(t, "mcp", entry.Provider)
}

func TestEnrichMCPAuditEntryBodyLoggingOff(t *testing.T) {
	entry := &auditlog.LogEntry{}
	c, _ := echotest.Post(t, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, echotest.WithValue(string(auditlog.LogEntryKey), entry))

	enrichMCPAuditEntry(c, false)

	if entry.Data != nil {
		require.Nil(t, entry.Data.RequestBody, "body logging is off")
	}
}

func TestMCPSSEAuditBody(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want func(t *testing.T, got any)
	}{
		{
			name: "single JSON-RPC frame decodes bare",
			raw:  "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n",
			want: func(t *testing.T, got any) {
				frame, ok := got.(map[string]any)
				require.True(t, ok, "got %T, want single decoded object", got)
				require.Equal(t, float64(1), frame["id"])

			},
		},
		{
			name: "several frames decode as a list",
			raw:  "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n\n",
			want: func(t *testing.T, got any) {
				frames, ok := got.([]any)
				require.True(t, ok)
				require.Len(t, frames, 2)

			},
		},
		{
			name: "truncated payload falls back to raw text",
			raw:  "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"resu",
			want: func(t *testing.T, got any) {
				_, ok := got.(string)
				require.True(t, ok, "got %T, want raw string fallback", got)

			},
		},
		{
			name: "no data lines is nil",
			raw:  ": keepalive\n\n",
			want: func(t *testing.T, got any) {
				require.Nil(t, got)

			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want(t, mcpSSEAuditBody([]byte(tt.raw)))
		})
	}
}
