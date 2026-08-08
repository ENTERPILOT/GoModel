package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestRedactSensitiveRequestURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "oauth callback", uri: "/sso/callback?code=secret-code&state=secret-state&error_description=provider-detail", want: "/sso/callback?code=REDACTED&error_description=REDACTED&state=REDACTED"},
		{name: "case insensitive", uri: "/callback?ID_TOKEN=secret", want: "/callback?ID_TOKEN=REDACTED"},
		{name: "ordinary query", uri: "/admin/usage?days=30&interval=daily", want: "/admin/usage?days=30&interval=daily"},
		{name: "malformed sensitive query", uri: "/callback?code=secret;broken", want: "/callback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.Use(redactSensitiveRequestURI())
			e.GET("/*", func(c *echo.Context) error {
				if tt.name == "oauth callback" && c.QueryParam("code") != "secret-code" {
					t.Fatalf("handler lost original query value")
				}
				if got := c.Request().RequestURI; got != tt.want {
					t.Fatalf("RequestURI = %q, want %q", got, tt.want)
				}
				return c.NoContent(http.StatusNoContent)
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.uri, nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}
