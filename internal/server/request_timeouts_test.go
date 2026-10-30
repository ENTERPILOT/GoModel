package server

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func TestRequestTimeoutHeaders(t *testing.T) {
	for _, value := range []string{"-1", "1.5", "no", "", "9223372036854775807", "1,2"} {
		t.Run(value, func(t *testing.T) {
			c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
			c.Request().Header.Set("X-GoModel-Request-Timeout-Ms", value)
			err := requestTimeoutOverrides()(func(*echo.Context) error { t.Error("invalid override reached handler"); return nil })(c)
			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, http.StatusBadRequest, httpErr.Code)
		})
	}
	upstream, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(80 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	})
	c, _ := echotest.Post(t, "/v1/chat/completions", `{}`)
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
