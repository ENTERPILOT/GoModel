package llmclient

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Providers take their transport from ProviderOptions, which is nil in
// production, so the nil case has to keep the shared pooled client rather than
// falling back to http.DefaultClient.
func TestNewWithOptionalHTTPClient(t *testing.T) {
	cfg := DefaultConfig("test", "http://example.invalid")

	t.Run("nil keeps the pooled default", func(t *testing.T) {
		withNil := NewWithOptionalHTTPClient(nil, cfg, nil)
		require.NotNil(t, withNil)
		plain := New(cfg, nil)
		require.NotNil(t, plain.httpClient)
		assert.NotNil(t, withNil.httpClient)
		assert.NotSame(t, http.DefaultClient, withNil.httpClient, "the pooled default, not http.DefaultClient")
	})

	t.Run("a given client is used", func(t *testing.T) {
		custom := &http.Client{}
		client := NewWithOptionalHTTPClient(custom, cfg, nil)
		require.NotNil(t, client)
		assert.Same(t, custom, client.httpClient)
	})
}
