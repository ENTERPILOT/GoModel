package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// TestClientIPExtractor checks the server keeps direct extraction until a
// deployment lists its proxies, and then reports the forwarded client
// everywhere c.RealIP() is read.
func TestClientIPExtractor(t *testing.T) {
	assert.Nil(t, ClientIPExtractor(config.ClientIPPolicy{}), "an unconfigured gateway must keep echo's direct extraction")

	cfg := config.ServerConfig{TrustedProxies: []string{"127.0.0.0/8"}}
	require.NoError(t, config.ResolveClientIPPolicy(&cfg))

	extractor := ClientIPExtractor(cfg.ClientIP)
	require.NotNil(t, extractor)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")
	assert.Equal(t, "198.51.100.7", extractor(req))
}

// TestNewUsesConfiguredIPExtractor checks the server installs the strategy it
// was handed rather than leaving echo's default in place.
func TestNewUsesConfiguredIPExtractor(t *testing.T) {
	cfg := config.ServerConfig{TrustedProxies: []string{"127.0.0.0/8"}}
	require.NoError(t, config.ResolveClientIPPolicy(&cfg))

	srv := New(nil, &Config{IPExtractor: ClientIPExtractor(cfg.ClientIP)})
	require.NotNil(t, srv)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	c := srv.echo.NewContext(req, httptest.NewRecorder())
	assert.Equal(t, "203.0.113.9", c.RealIP())
}
