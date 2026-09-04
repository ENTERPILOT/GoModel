package httpclient

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetTLS(t *testing.T) {
	t.Helper()
	trust.Store(nil)
	t.Cleanup(func() { trust.Store(nil) })
}

func TestSetConfiguredTLS_ZeroValueKeepsSystemDefaults(t *testing.T) {
	resetTLS(t)
	if err := SetConfiguredTLS(TLSSettings{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ConfiguredTLS() != nil {
		t.Fatal("zero settings should leave system defaults (nil tls.Config)")
	}
	transport := underlyingTransport(t, NewDefaultHTTPClient())
	if transport.TLSClientConfig != nil {
		t.Fatal("transport should not carry a tls.Config on system defaults")
	}
}

func TestSetConfiguredTLS_CAFileTrustsPrivateCA(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Without the CA the self-signed test server must be rejected.
	if _, err := NewClientWithTimeout(5 * time.Second).Get(srv.URL); err == nil {
		t.Fatal("expected certificate error without trusted CA")
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pemEncode(t, srv.Certificate().Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetConfiguredTLS(TLSSettings{CAFile: caFile}); err != nil {
		t.Fatalf("SetConfiguredTLS: %v", err)
	}
	resp, err := NewClientWithTimeout(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("expected private CA to be trusted, got %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}

func TestSetConfiguredTLS_Errors(t *testing.T) {
	resetTLS(t)
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
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
			if err := SetConfiguredTLS(tt.settings); err == nil {
				t.Fatal("expected an error")
			}
			if ConfiguredTLS() != nil {
				t.Fatal("a failed install must not replace the previous configuration")
			}
		})
	}
}

func TestSetConfiguredTLS_InsecureSkipVerify(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	cfg := ConfiguredTLS()
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify to be set")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS 1.2 floor, got %d", cfg.MinVersion)
	}
	resp, err := NewDefaultHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("expected the self-signed server to be accepted: %v", err)
	}
	resp.Body.Close()
}

func TestNewClientWithTimeout(t *testing.T) {
	c := NewClientWithTimeout(7 * time.Second)
	if c.Timeout != 7*time.Second {
		t.Fatalf("Timeout = %v, want 7s", c.Timeout)
	}
	transport := underlyingTransport(t, c)
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.Proxy == nil {
		t.Fatal("expected proxy-from-environment on the shared transport")
	}
	if NewClientWithTimeout(0).Timeout != DefaultConfig().Timeout {
		t.Fatal("non-positive timeout should keep the default")
	}
}

func TestSnapshotAndRestoreTLS(t *testing.T) {
	resetTLS(t)
	if err := SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	serving := SnapshotTLS()
	if err := SetConfiguredTLS(TLSSettings{}); err != nil {
		t.Fatal(err)
	}
	if ConfiguredTLS() != nil {
		t.Fatal("replacement should have installed system defaults")
	}
	RestoreTLS(serving)
	cfg := ConfiguredTLS()
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatal("restore should reinstall the serving generation's configuration")
	}
}

func TestNewAWSSDKClientFollowsOnly307And308(t *testing.T) {
	client := NewAWSSDKClient()
	for code, follow := range map[int]bool{301: false, 302: false, 303: false, 307: true, 308: true} {
		req := &http.Request{Response: &http.Response{StatusCode: code}}
		err := client.CheckRedirect(req, nil)
		if follow && err != nil {
			t.Errorf("%d should be followed, got %v", code, err)
		}
		if !follow && err != http.ErrUseLastResponse {
			t.Errorf("%d should not be followed, got %v", code, err)
		}
	}
}

func TestNewAWSSDKClientBoundsRedirectLoops(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	client := NewAWSSDKClient()
	client.Timeout = 10 * time.Second
	resp, err := client.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("expected redirect limit error, got %v", err)
	}
	if got := hops.Load(); got != maxRedirects {
		t.Errorf("server saw %d requests, want %d", got, maxRedirects)
	}
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
	if err := SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	captured := NewDefaultHTTPClient()
	if resp, err := captured.Get(srv.URL); err != nil {
		t.Fatalf("self-signed server should be accepted while skip-verify is installed: %v", err)
	} else {
		resp.Body.Close()
	}

	// The reload is rejected and the serving trust comes back.
	RestoreTLS(before)
	for name, c := range map[string]*http.Client{"captured": captured, "serving": serving} {
		resp, err := c.Get(srv.URL)
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("%s client should reject the self-signed server after rollback, got %v", name, err)
		}
	}
	if underlyingTransport(t, serving) != servingRT {
		t.Error("serving client was rebuilt although its trust configuration was restored unchanged")
	}
}

func TestClientPicksUpTLSChangeWithoutRecreation(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pemEncode(t, srv.Certificate().Raw), 0o600); err != nil {
		t.Fatal(err)
	}

	client := NewDefaultHTTPClient()
	if resp, err := client.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected the private CA to be rejected before it is trusted")
	}
	if err := SetConfiguredTLS(TLSSettings{CAFile: caFile}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("existing client should trust the CA after SetConfiguredTLS: %v", err)
	}
	resp.Body.Close()
}
