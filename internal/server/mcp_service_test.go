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

func TestEnrichMCPAuditEntryRestoresBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	e := echo.New()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	enrichMCPAuditEntry(c)

	restored, err := io.ReadAll(c.Request().Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored body = %q, want %q (must reach the MCP handler intact)", restored, body)
	}
}
