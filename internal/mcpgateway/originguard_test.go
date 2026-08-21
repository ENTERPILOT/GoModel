package mcpgateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestOriginGuardCheck(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		method  string
		headers map[string]string
		allowed bool
	}{
		{
			name:    "mcp client sends no browser metadata",
			method:  http.MethodPost,
			allowed: true,
		},
		{
			name:    "dns rebound request has a self-consistent origin and host",
			method:  http.MethodPost,
			headers: map[string]string{"Host": "attacker.example:8080", "Origin": "http://attacker.example:8080"},
			allowed: false,
		},
		{
			name:   "dns rebound request reports itself as same-origin",
			method: http.MethodPost,
			headers: map[string]string{
				"Host": "attacker.example:8080", "Origin": "http://attacker.example:8080",
				"Sec-Fetch-Site": "same-origin",
			},
			allowed: false,
		},
		{
			name:    "GET opens the notification stream and is checked too",
			method:  http.MethodGet,
			headers: map[string]string{"Host": "attacker.example:8080", "Origin": "http://attacker.example:8080", "Sec-Fetch-Site": "same-origin"},
			allowed: false,
		},
		{
			name:    "DELETE tears a session down and is checked too",
			method:  http.MethodDelete,
			headers: map[string]string{"Host": "attacker.example:8080", "Origin": "http://attacker.example:8080"},
			allowed: false,
		},
		{
			name:    "browser metadata without any origin cannot be trusted",
			method:  http.MethodPost,
			headers: map[string]string{"Sec-Fetch-Site": "none"},
			allowed: false,
		},
		{
			name:    "opaque origin never matches",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "null"},
			allowed: false,
		},
		{
			name:    "trusted origin is served",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "https://console.example.com", "Sec-Fetch-Site": "same-origin"},
			allowed: true,
		},
		{
			name:    "trusted origin matches case-insensitively",
			trusted: []string{"https://Console.Example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "HTTPS://console.example.COM"},
			allowed: true,
		},
		{
			name:    "trusting one origin does not trust a look-alike",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "https://console.example.com.attacker.example"},
			allowed: false,
		},
		{
			name:    "port is part of the origin",
			trusted: []string{"http://localhost:3000"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://localhost:3001"},
			allowed: false,
		},
		{
			name:    "scheme is part of the origin",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://console.example.com"},
			allowed: false,
		},
		{
			name:    "allowlisted host may open the origin-less same-origin stream",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "same-origin"},
			allowed: true,
		},
		{
			name:    "HEAD is treated like the stream GET",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodHead,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "same-origin"},
			allowed: true,
		},
		{
			name:    "allowlisted host on a non-default port matches the Host header",
			trusted: []string{"http://localhost:3000"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "localhost:3000", "Sec-Fetch-Site": "same-origin"},
			allowed: true,
		},
		{
			name:    "rebound host is not on the allowlist so the stream is refused",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "attacker.example:8080", "Sec-Fetch-Site": "same-origin"},
			allowed: false,
		},
		{
			name:    "origin-less stream is refused when nothing is allowlisted",
			method:  http.MethodGet,
			headers: map[string]string{"Host": "gateway.internal:8080", "Sec-Fetch-Site": "same-origin"},
			allowed: false,
		},
		{
			name:    "same-site does not attest the initiating origin",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "same-site"},
			allowed: false,
		},
		{
			name:    "cross-site does not attest the initiating origin",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "cross-site"},
			allowed: false,
		},
		{
			name:    "a bare navigation to the endpoint is still refused",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "none"},
			allowed: false,
		},
		{
			name:    "the host fallback never applies to unsafe methods",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Host": "console.example.com", "Sec-Fetch-Site": "same-origin"},
			allowed: false,
		},
		{
			name:    "an explicitly rejected Origin is not rescued by a trusted Host",
			trusted: []string{"https://console.example.com"},
			method:  http.MethodGet,
			headers: map[string]string{"Host": "console.example.com", "Origin": "https://attacker.example", "Sec-Fetch-Site": "same-origin"},
			allowed: false,
		},
		{
			name:    "default https port is canonicalized on both sides",
			trusted: []string{"https://console.example.com:443"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "https://console.example.com"},
			allowed: true,
		},
		{
			name:    "default http port is canonicalized on both sides",
			trusted: []string{"http://console.example.com"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://console.example.com:80"},
			allowed: true,
		},
		{
			name:    "a default port is not stripped for the other scheme",
			trusted: []string{"https://console.example.com:80"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "https://console.example.com"},
			allowed: false,
		},
		{
			name:    "wildcard trusts every origin",
			trusted: []string{"*"},
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://attacker.example:8080", "Sec-Fetch-Site": "same-origin"},
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, err := newOriginGuard(tt.trusted)
			if err != nil {
				t.Fatalf("newOriginGuard(%v) error: %v", tt.trusted, err)
			}
			req := httptest.NewRequest(tt.method, "/mcp", nil)
			for key, value := range tt.headers {
				if key == "Host" {
					req.Host = value
					continue
				}
				req.Header.Set(key, value)
			}

			err = guard.check(req)
			if tt.allowed {
				if err != nil {
					t.Fatalf("check() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("check() = nil, want a rejection")
			}
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("check() = %T, want *core.GatewayError", err)
			}
			if gatewayErr.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", gatewayErr.StatusCode, http.StatusForbidden)
			}
			if origin := tt.headers["Origin"]; origin != "" && strings.Contains(gatewayErr.Message, origin) {
				t.Fatalf("message echoes the rejected origin: %q", gatewayErr.Message)
			}
		})
	}
}

func TestNewOriginGuardRejectsMalformedEntries(t *testing.T) {
	for _, entry := range []string{
		"console.example.com",              // no scheme
		"https://",                         // no host
		"https://console.example.com/mcp",  // path
		"https://console.example.com?a=b",  // query
		"https://user@console.example.com", // userinfo
	} {
		t.Run(entry, func(t *testing.T) {
			if _, err := newOriginGuard([]string{entry}); err == nil {
				t.Fatalf("newOriginGuard(%q) = nil error, want a rejection", entry)
			}
		})
	}
}

func TestNewOriginGuardIgnoresBlankEntries(t *testing.T) {
	guard, err := newOriginGuard([]string{"", "  "})
	if err != nil {
		t.Fatalf("newOriginGuard() error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "http://attacker.example:8080")
	if err := guard.check(req); err == nil {
		t.Fatal("check() = nil, want a rejection")
	}
}

// nonLoopbackAddr is the local address of a gateway bound to all interfaces,
// which is what `net.Listen("tcp", ":8080")` produces for a LAN caller. The
// MCP SDK's own DNS-rebinding guard is scoped to loopback local addresses by
// design, so it never engages here and the gateway's guard is the only
// defense — the exact condition the advisory describes.
type nonLoopbackAddr struct{}

func (nonLoopbackAddr) Network() string { return "tcp" }
func (nonLoopbackAddr) String() string  { return "192.0.2.10:8080" }

const initializeFrame = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"poc","version":"0"}}}`

func newInitializeRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeFrame))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, nonLoopbackAddr{}))
}

// TestServiceRejectsReboundRequestOnNonLoopbackBind reproduces the advisory's
// proof of concept: a self-consistent forged Host/Origin pair, no credential,
// arriving on a non-loopback local address.
func TestServiceRejectsReboundRequestOnNonLoopbackBind(t *testing.T) {
	upstream := newTestUpstream(t, "alpha", addEchoTool("ping"))
	service, _ := newTestService(t, nil, testSpec("alpha", upstream, nil))

	req := newInitializeRequest(t)
	req.Host = "attacker.example:8080"
	req.Header.Set("Origin", "http://attacker.example:8080")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	rec := httptest.NewRecorder()
	err := service.ServeHTTP(rec, req, "")
	if err == nil {
		t.Fatal("ServeHTTP() = nil, want a rejection; the rebound request established a session")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.StatusCode != http.StatusForbidden {
		t.Fatalf("ServeHTTP() = %v, want a 403 *core.GatewayError", err)
	}
	if sessionID := rec.Header().Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("rejected request was issued session %q", sessionID)
	}
}

// TestServiceServesMCPClientOnNonLoopbackBind is the other half: the guard
// must not cost ordinary MCP clients, which send no browser fetch metadata,
// their session on that same bind.
func TestServiceServesMCPClientOnNonLoopbackBind(t *testing.T) {
	upstream := newTestUpstream(t, "alpha", addEchoTool("ping"))
	service, _ := newTestService(t, nil, testSpec("alpha", upstream, nil))

	req := newInitializeRequest(t)
	req.Host = "gateway.internal:8080"

	rec := httptest.NewRecorder()
	if err := service.ServeHTTP(rec, req, ""); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Header().Get("Mcp-Session-Id") == "" {
		t.Fatal("no Mcp-Session-Id issued to a legitimate MCP client")
	}
}
