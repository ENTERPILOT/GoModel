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

	"github.com/enterpilot/gomodel/internal/auditlog"
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
			if got := mcpAuditLabel([]byte(tt.body)); got != tt.want {
				t.Fatalf("mcpAuditLabel(%s) = %q, want %q", tt.body, got, tt.want)
			}
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

// rejectingBudgetChecker refuses every user path.
type rejectingBudgetChecker struct{}

func (rejectingBudgetChecker) Check(context.Context, string, time.Time) error {
	return context.DeadlineExceeded
}

func newEmptyMCPGateway(t *testing.T) *mcpgateway.Service {
	t.Helper()
	gateway, err := mcpgateway.NewService(context.Background(), mcpgateway.Options{})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(gateway.Close)
	return gateway
}

const mcpInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func newMCPTestContext(method, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/mcp", reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func errorCodeFromBody(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return envelope.Error.Code
}

// TestMCPServiceHandleGates covers the /mcp admission path: the feature gate,
// rate-limit and budget enforcement on POSTs, and delegation to the gateway.
func TestMCPServiceHandleGates(t *testing.T) {
	t.Run("disabled gateway is 501", func(t *testing.T) {
		svc := &mcpService{enabled: false}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501", rec.Code)
		}
	})

	t.Run("nil gateway is 501 even when enabled", func(t *testing.T) {
		svc := &mcpService{enabled: true}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501", rec.Code)
		}
	})

	t.Run("rate limit breach is 429 and never reaches the gateway", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, rateLimiter: rejectingRateLimiter{}}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 body=%s", rec.Code, rec.Body.String())
		}
		if code := errorCodeFromBody(t, rec.Body.Bytes()); code != "rate_limit_exceeded" {
			t.Fatalf("error code = %q, want rate_limit_exceeded", code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("429 response missing Retry-After header")
		}
		if rec.Header().Get("Mcp-Session-Id") != "" {
			t.Fatalf("gateway ran despite the rate-limit breach")
		}
	})

	t.Run("budget rejection blocks after rate limiting", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, budgetChecker: rejectingBudgetChecker{}}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (budget check failed) body=%s", rec.Code, rec.Body.String())
		}
		if code := errorCodeFromBody(t, rec.Body.Bytes()); code != "budget_check_failed" {
			t.Fatalf("error code = %q, want budget_check_failed", code)
		}
		if rec.Header().Get("Mcp-Session-Id") != "" {
			t.Fatalf("gateway ran despite the budget rejection")
		}
	})

	t.Run("happy path delegates to the gateway", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Mcp-Session-Id") == "" {
			t.Fatalf("initialize response missing Mcp-Session-Id; delegation did not happen")
		}
	})

	t.Run("body logging captures the JSON-RPC exchange on the audit entry", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, logBodies: true}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		entry := &auditlog.LogEntry{}
		c.Set(string(auditlog.LogEntryKey), entry)

		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		if entry.Data == nil || entry.Data.RequestBody == nil {
			t.Fatalf("request body missing from the audit entry")
		}
		if entry.Data.ResponseBody == nil {
			t.Fatalf("response body missing from the audit entry (content-type %q, body %q)",
				rec.Header().Get("Content-Type"), rec.Body.String())
		}
		frame, ok := entry.Data.ResponseBody.(map[string]any)
		if !ok {
			t.Fatalf("response body type = %T, want decoded JSON-RPC frame", entry.Data.ResponseBody)
		}
		if frame["jsonrpc"] != "2.0" {
			t.Fatalf("response frame = %v, want a JSON-RPC message", frame)
		}
	})

	t.Run("body logging off leaves the audit entry without bodies", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, logBodies: false}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		entry := &auditlog.LogEntry{}
		c.Set(string(auditlog.LogEntryKey), entry)

		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		if entry.Data != nil && entry.Data.RequestBody != nil {
			t.Fatalf("request body captured with body logging off: %v", entry.Data.RequestBody)
		}
		if entry.Data != nil && entry.Data.ResponseBody != nil {
			t.Fatalf("response body captured with body logging off: %v", entry.Data.ResponseBody)
		}
	})

	t.Run("GET skips the admission gates", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true, rateLimiter: rejectingRateLimiter{}}
		c, rec := newMCPTestContext(http.MethodGet, "")
		if err := svc.handle(c, ""); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		// The SDK rejects the sessionless GET itself; the point is that the
		// breaching rate limiter never turned it into a 429.
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("GET was rate limited; the notification stream must not consume request budget")
		}
	})

	t.Run("unknown pinned server is 404", func(t *testing.T) {
		svc := &mcpService{gateway: newEmptyMCPGateway(t), enabled: true}
		c, rec := newMCPTestContext(http.MethodPost, mcpInitializeBody)
		if err := svc.handle(c, "ghost"); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
		}
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
				if err != nil {
					t.Fatalf("Write() error = %v", err)
				}
				forwarded += n
			}
			if got := capture.body.String(); got != tt.wantCaptured {
				t.Fatalf("captured %d bytes, want %d", len(got), len(tt.wantCaptured))
			}
			if capture.truncated != tt.wantTruncated {
				t.Fatalf("truncated = %v, want %v", capture.truncated, tt.wantTruncated)
			}
			if want := len(strings.Join(tt.writes, "")); rec.Body.Len() != want || forwarded != want {
				t.Fatalf("client received %d bytes (Write reported %d), want %d — the tee must never cut the response",
					rec.Body.Len(), forwarded, want)
			}
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
			c, rec := newMCPTestContext(http.MethodPost, "")
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)
			capture := &mcpResponseCapture{ResponseWriter: rec}
			capture.Header().Set("Content-Type", tt.contentType)
			if _, err := capture.Write([]byte(tt.body)); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			capture.truncated = tt.truncated

			capture.enrich(c)

			gotBody := entry.Data != nil && entry.Data.ResponseBody != nil
			if gotBody != tt.wantBody {
				t.Fatalf("response body recorded = %v, want %v", gotBody, tt.wantBody)
			}
			gotTruncated := entry.Data != nil && entry.Data.ResponseBodyTooBigToHandle
			if gotTruncated != tt.wantTruncated {
				t.Fatalf("truncation flag = %v, want %v", gotTruncated, tt.wantTruncated)
			}
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
			if got := h.mcp().logBodies; got != tt.want {
				t.Fatalf("mcp().logBodies = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnrichMCPAuditEntryRestoresBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	e := echo.New()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	enrichMCPAuditEntry(c, false)

	restored, err := io.ReadAll(c.Request().Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored body = %q, want %q (must reach the MCP handler intact)", restored, body)
	}
}

func TestEnrichMCPAuditEntryCapturesRequestBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"linear_list_issues"}}`
	e := echo.New()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)

	enrichMCPAuditEntry(c, true)

	if entry.Data == nil || entry.Data.RequestBody == nil {
		t.Fatalf("request body was not captured on the audit entry")
	}
	captured, ok := entry.Data.RequestBody.(map[string]any)
	if !ok {
		t.Fatalf("captured body type = %T, want parsed JSON object", entry.Data.RequestBody)
	}
	if captured["method"] != "tools/call" {
		t.Fatalf("captured method = %v, want tools/call", captured["method"])
	}
	if entry.RequestedModel != "linear_list_issues" || entry.Provider != "mcp" {
		t.Fatalf("entry label = (%q, %q), want (linear_list_issues, mcp)", entry.RequestedModel, entry.Provider)
	}
}

func TestEnrichMCPAuditEntryBodyLoggingOff(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)

	enrichMCPAuditEntry(c, false)

	if entry.Data != nil && entry.Data.RequestBody != nil {
		t.Fatalf("request body captured with body logging off: %v", entry.Data.RequestBody)
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
				if !ok {
					t.Fatalf("got %T, want single decoded object", got)
				}
				if frame["id"] != float64(1) {
					t.Fatalf("frame id = %v, want 1", frame["id"])
				}
			},
		},
		{
			name: "several frames decode as a list",
			raw:  "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n\n",
			want: func(t *testing.T, got any) {
				frames, ok := got.([]any)
				if !ok || len(frames) != 2 {
					t.Fatalf("got %T (%v), want list of 2 frames", got, got)
				}
			},
		},
		{
			name: "truncated payload falls back to raw text",
			raw:  "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"resu",
			want: func(t *testing.T, got any) {
				if _, ok := got.(string); !ok {
					t.Fatalf("got %T, want raw string fallback", got)
				}
			},
		},
		{
			name: "no data lines is nil",
			raw:  ": keepalive\n\n",
			want: func(t *testing.T, got any) {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want(t, mcpSSEAuditBody([]byte(tt.raw)))
		})
	}
}
