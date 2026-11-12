package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func TestRequestTimeoutHeaders(t *testing.T) {
	for _, header := range []string{"X-GoModel-Request-Timeout-Ms", "X-GoModel-Response-Header-Timeout-Ms"} {
		for _, values := range [][]string{{"-1"}, {"1.5"}, {"no"}, {""}, {"9223372036854775807"}, {"1,2"}, {"1", "2"}} {
			t.Run(header+"="+strings.Join(values, ","), func(t *testing.T) {
				c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
				for _, value := range values {
					c.Request().Header.Add(header, value)
				}
				err := requestTimeoutOverrides()(func(*echo.Context) error { t.Error("invalid override reached handler"); return nil })(c)
				var httpErr *echo.HTTPError
				require.ErrorAs(t, err, &httpErr)
				require.Equal(t, http.StatusBadRequest, httpErr.Code)
			})
		}
	}
	upstream, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(80 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	})
	c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
	c.Set(requestTimeoutAuthenticatedKey, true)
	c.Request().Header.Set("X-GoModel-Request-Timeout-Ms", "0")
	c.Request().Header.Set("X-GoModel-Response-Header-Timeout-Ms", "0")
	cfg := httpclient.DefaultConfig()
	cfg.Timeout, cfg.ResponseHeaderTimeout = 10*time.Millisecond, 10*time.Millisecond
	client := httpclient.NewHTTPClient(&cfg)
	defer client.CloseIdleConnections()
	err := requestTimeoutOverrides()(func(c *echo.Context) error {
		require.Empty(t, c.Request().Header.Get("X-GoModel-Request-Timeout-Ms"))
		require.Empty(t, c.Request().Header.Get("X-GoModel-Response-Header-Timeout-Ms"))
		req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, upstream.URL, nil)
		require.NoError(t, err)
		resp, err := httpclient.Do(client, req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, "ok", string(body))
		return nil
	})(c)
	require.NoError(t, err)
}

func TestRequestTimeoutHeadersOmittedPreserveRequest(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
	original := c.Request()
	err := requestTimeoutOverrides()(func(c *echo.Context) error {
		require.Same(t, original, c.Request(), "ordinary requests must not allocate a replacement context/request")
		return nil
	})(c)
	require.NoError(t, err)
}

func TestRequestTimeoutHeadersRequireSuccessfulAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name          string
		auth          AuthMiddlewareConfig
		token         string
		authenticated bool
	}{
		{name: "open mode"},
		{name: "disabled authenticator", auth: AuthMiddlewareConfig{Authenticator: mockAuthenticator{}}},
		{name: "skipped path", auth: AuthMiddlewareConfig{MasterKey: "test-key", SkipPaths: []string{"/v1/chat/completions"}}},
		{name: "master key", auth: AuthMiddlewareConfig{MasterKey: "test-key"}, token: "test-key", authenticated: true},
		{name: "managed key", auth: AuthMiddlewareConfig{Authenticator: mockAuthenticator{enabled: true, tokenToID: map[string]string{"test-key": "key-id"}}}, token: "test-key", authenticated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
			if tc.token != "" {
				c.Request().Header.Set("Authorization", "Bearer "+tc.token)
			}
			c.Request().Header.Set("X-GoModel-Request-Timeout-Ms", "0")
			c.Request().Header.Set("X-GoModel-Response-Header-Timeout-Ms", "0")
			var original *http.Request
			handler := requestTimeoutOverrides()(func(c *echo.Context) error {
				require.Empty(t, c.Request().Header.Get("X-GoModel-Request-Timeout-Ms"))
				require.Empty(t, c.Request().Header.Get("X-GoModel-Response-Header-Timeout-Ms"))
				if tc.authenticated {
					require.NotSame(t, original, c.Request(), "authenticated override must attach its context")
				} else {
					require.Same(t, original, c.Request(), "anonymous override must not attach a context")
				}
				return nil
			})
			err := NewAuthMiddleware(tc.auth)(func(c *echo.Context) error {
				original = c.Request()
				return handler(c)
			})(c)
			require.NoError(t, err)
		})
	}
}

func TestRequestTimeoutHeadersInheritIndependently(t *testing.T) {
	upstream, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(80 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	})
	for _, tc := range []struct {
		name, header string
		total, wait  time.Duration
		wantError    bool
	}{
		{"overall only", "X-GoModel-Request-Timeout-Ms", 10 * time.Millisecond, time.Second, false},
		{"header only", "X-GoModel-Response-Header-Timeout-Ms", time.Second, 10 * time.Millisecond, false},
		{"inherit header", "X-GoModel-Request-Timeout-Ms", time.Second, 10 * time.Millisecond, true},
		{"inherit overall", "X-GoModel-Response-Header-Timeout-Ms", 10 * time.Millisecond, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
			c.Set(requestTimeoutAuthenticatedKey, true)
			c.Request().Header.Set(tc.header, "0")
			cfg := httpclient.DefaultConfig()
			cfg.Timeout, cfg.ResponseHeaderTimeout = tc.total, tc.wait
			client := httpclient.NewHTTPClient(&cfg)
			defer client.CloseIdleConnections()
			err := requestTimeoutOverrides()(func(c *echo.Context) error {
				req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, upstream.URL, nil)
				require.NoError(t, err)
				resp, err := httpclient.Do(client, req)
				if tc.wantError {
					require.Error(t, err)
					return nil
				}
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				return nil
			})(c)
			require.NoError(t, err)
		})
	}
}
