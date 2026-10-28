package run

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/servertls"
	"github.com/enterpilot/gomodel/internal/tlstest"
)

// The container HEALTHCHECK runs `gomodel --health`, so the probe has to get
// through the listener whatever TLS it serves: a certificate issued for the
// service name rather than 127.0.0.1, and client certificates when required.
func TestProbeClientReachesTLSListener(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway.example.svc").Write(t, dir)
	caFile := filepath.Join(dir, "ca.crt")
	tlstest.WriteFile(t, caFile, ca.PEM)

	for name, clientCA := range map[string]string{"server TLS": "", "mutual TLS": caFile} {
		t.Run(name, func(t *testing.T) {
			settings := config.ServerConfig{TLSCertFile: certFile, TLSKeyFile: keyFile, TLSClientCAFile: clientCA}
			source, err := servertls.New(servertls.Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: clientCA})
			require.NoError(t, err)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			srv := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, `{"status":"ok"}`)
				}),
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() { _ = srv.Serve(tls.NewListener(ln, source.ServerConfig())) }()
			t.Cleanup(func() { _ = srv.Close() })

			client, err := probeClient(settings, 5*time.Second)
			require.NoError(t, err)
			require.NoError(t, checkHealthEndpoint(t.Context(), client, "https://"+ln.Addr().String()+"/health"))
		})
	}
}
