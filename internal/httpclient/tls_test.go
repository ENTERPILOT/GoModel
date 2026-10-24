package httpclient

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetTLS(t *testing.T) {
	t.Helper()
	trust.Store(nil)
	t.Cleanup(func() { trust.Store(nil) })
}

func TestSetConfiguredTLS_ZeroValueKeepsSystemDefaults(t *testing.T) {
	resetTLS(t)
	require.NoError(t, SetConfiguredTLS(TLSSettings{}))
	require.Nil(t, ConfiguredTLS(), "zero settings should leave system defaults (nil tls.Config)")
	transport := underlyingTransport(t, NewDefaultHTTPClient())
	assert.Nil(t, transport.TLSClientConfig, "transport should not carry a tls.Config on system defaults")
}

func TestSetConfiguredTLS_CAFileTrustsPrivateCA(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Without the CA the self-signed test server must be rejected.
	_, err := NewClientWithTimeout(5 * time.Second).Get(srv.URL)
	require.Error(t, err, "expected certificate error without trusted CA")

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pemEncode(t, srv.Certificate().Raw), 0o600))
	require.NoError(t, SetConfiguredTLS(TLSSettings{CAFile: caFile}))
	resp, err := NewClientWithTimeout(5 * time.Second).Get(srv.URL)
	require.NoError(t, err, "expected private CA to be trusted")
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestSetConfiguredTLS_Errors(t *testing.T) {
	resetTLS(t)
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	require.NoError(t, os.WriteFile(empty, []byte("not a certificate"), 0o600))
	tests := []struct {
		name     string
		settings TLSSettings
	}{
		{"missing ca file", TLSSettings{CAFile: filepath.Join(dir, "missing.pem")}},
		{"ca file without certificates", TLSSettings{CAFile: empty}},
		{"client cert without key", TLSSettings{ClientCertFile: empty}},
		{"client key without cert", TLSSettings{ClientKeyFile: empty}},
		{"unparseable client pair", TLSSettings{ClientCertFile: empty, ClientKeyFile: empty}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, SetConfiguredTLS(tt.settings))
			assert.Nil(t, ConfiguredTLS(), "a failed install must not replace the previous configuration")
		})
	}
}

func TestSetConfiguredTLS_InsecureSkipVerify(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	require.NoError(t, SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}))
	cfg := ConfiguredTLS()
	require.NotNil(t, cfg)
	assert.True(t, cfg.InsecureSkipVerify, "expected InsecureSkipVerify to be set")
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion, "expected TLS 1.2 floor")
	resp, err := NewDefaultHTTPClient().Get(srv.URL)
	require.NoError(t, err, "expected the self-signed server to be accepted")
	require.NoError(t, resp.Body.Close())
}

func TestNewClientWithTimeout(t *testing.T) {
	c := NewClientWithTimeout(7 * time.Second)
	assert.Equal(t, 7*time.Second, c.Timeout)
	transport := underlyingTransport(t, c)
	assert.Equal(t, 7*time.Second, transport.ResponseHeaderTimeout)
	assert.NotNil(t, transport.Proxy, "expected proxy-from-environment on the shared transport")
	assert.Equal(t, DefaultConfig().Timeout, NewClientWithTimeout(0).Timeout, "non-positive timeout should keep the default")
}

func TestSnapshotAndRestoreTLS(t *testing.T) {
	resetTLS(t)
	require.NoError(t, SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}))
	serving := SnapshotTLS()
	require.NoError(t, SetConfiguredTLS(TLSSettings{}))
	require.Nil(t, ConfiguredTLS(), "replacement should have installed system defaults")
	RestoreTLS(serving)
	cfg := ConfiguredTLS()
	require.NotNil(t, cfg, "restore should reinstall the serving generation's configuration")
	assert.True(t, cfg.InsecureSkipVerify)
}

func TestNewAWSSDKClientFollowsOnly307And308(t *testing.T) {
	client := NewAWSSDKClient(nil)
	origin := &http.Request{URL: &url.URL{Scheme: "https", Host: "bedrock.us-east-1.amazonaws.com"}}
	for code, follow := range map[int]bool{301: false, 302: false, 303: false, 307: true, 308: true} {
		req := &http.Request{
			URL:      &url.URL{Scheme: "https", Host: "bedrock.us-east-1.amazonaws.com", Path: "/next"},
			Response: &http.Response{StatusCode: code},
		}
		err := client.CheckRedirect(req, []*http.Request{origin})
		if follow {
			assert.NoError(t, err, "%d should be followed", code)
		} else {
			assert.ErrorIs(t, err, http.ErrUseLastResponse, "%d should not be followed", code)
		}
	}
}

func TestNewAWSSDKClientStopsCrossOriginRedirects(t *testing.T) {
	client := NewAWSSDKClient(nil)
	origin := &http.Request{URL: &url.URL{Scheme: "https", Host: "bedrock.us-east-1.amazonaws.com"}}
	req := &http.Request{
		URL:      &url.URL{Scheme: "https", Host: "evil.test"},
		Response: &http.Response{StatusCode: http.StatusTemporaryRedirect},
	}
	assert.ErrorIs(t, client.CheckRedirect(req, []*http.Request{origin}), http.ErrUseLastResponse)
}

func TestNewAWSSDKClientKeepsTheBaseTransport(t *testing.T) {
	base := NewDefaultHTTPClient()
	client := NewAWSSDKClient(base)
	assert.NotSame(t, base, client, "the base client's redirect policy must stay untouched")
	assert.Same(t, base.Transport, client.Transport)
}

func TestNewAWSSDKClientBoundsRedirectLoops(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	client := NewAWSSDKClient(nil)
	client.Timeout = 10 * time.Second
	resp, err := client.Get(srv.URL)
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.ErrorContains(t, err, "stopped after 10 redirects")
	assert.Equal(t, int32(maxRedirects), hops.Load())
}

// A client created while a later-rejected reload had its TLS settings
// installed must follow the rollback rather than keep the rejected policy.
func TestClientFollowsTLSRollback(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	serving := NewDefaultHTTPClient()
	servingRT := underlyingTransport(t, serving)
	before := SnapshotTLS()

	// Replacement settings go in, and a client is built while they are live.
	require.NoError(t, SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}))
	captured := NewDefaultHTTPClient()
	resp, err := captured.Get(srv.URL)
	require.NoError(t, err, "self-signed server should be accepted while skip-verify is installed")
	require.NoError(t, resp.Body.Close())

	// The reload is rejected and the serving trust comes back.
	RestoreTLS(before)
	for name, c := range map[string]*http.Client{"captured": captured, "serving": serving} {
		resp, err := c.Get(srv.URL)
		if resp != nil {
			require.NoError(t, resp.Body.Close())
		}
		require.ErrorContains(t, err, "certificate", "%s client should reject the self-signed server after rollback", name)
	}
	assert.Same(t, servingRT, underlyingTransport(t, serving),
		"serving client was rebuilt although its trust configuration was restored unchanged")
}

func TestClientPicksUpTLSChangeWithoutRecreation(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pemEncode(t, srv.Certificate().Raw), 0o600))

	client := NewDefaultHTTPClient()
	resp, err := client.Get(srv.URL)
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.Error(t, err, "expected the private CA to be rejected before it is trusted")
	require.NoError(t, SetConfiguredTLS(TLSSettings{CAFile: caFile}))
	resp, err = client.Get(srv.URL)
	require.NoError(t, err, "existing client should trust the CA after SetConfiguredTLS")
	require.NoError(t, resp.Body.Close())
}
