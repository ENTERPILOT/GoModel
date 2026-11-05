package ext

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

func TestOutboundTLSFollowsCoreSettings(t *testing.T) {
	before := httpclient.SnapshotTLS()
	t.Cleanup(func() { httpclient.RestoreTLS(before) })
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	require.NoError(t, httpclient.SetConfiguredTLS(httpclient.TLSSettings{}))
	assert.Nil(t, OutboundTLSConfig(), "system defaults have no TLS config")
	client := NewHTTPClient(5 * time.Second)
	assert.Equal(t, 5*time.Second, client.Timeout)
	resp, err := client.Get(srv.URL)
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.Error(t, err, "the self-signed server must be rejected on system defaults")

	require.NoError(t, httpclient.SetConfiguredTLS(httpclient.TLSSettings{InsecureSkipVerify: true}))
	cfg := OutboundTLSConfig()
	require.NotNil(t, cfg)
	assert.True(t, cfg.InsecureSkipVerify)
	resp, err = client.Get(srv.URL)
	require.NoError(t, err, "an existing extension client must follow the installed trust")
	require.NoError(t, resp.Body.Close())
}
