package server

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
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
