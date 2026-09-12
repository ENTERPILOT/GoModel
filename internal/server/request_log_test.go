package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				if tt.name == "oauth callback" {
					assert.Equal(t, "secret-code", c.QueryParam("code"), "handler lost original query value")
				}
				assert.Equal(t, tt.want, c.Request().RequestURI)
				return c.NoContent(http.StatusNoContent)
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.uri, nil))
			require.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}
