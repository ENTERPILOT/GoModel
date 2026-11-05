package run

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// Extensions set up, and secret references resolve, before the application is
// built; a secret store behind a private CA needs the configured trust by
// then. A generation rejected at that point must not leave its trust behind.
func TestRunInstallsOutboundTLSBeforeExtensionSetup(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HTTP_TLS_INSECURE_SKIP_VERIFY", "true")
	before := httpclient.SnapshotTLS()
	t.Cleanup(func() { httpclient.RestoreTLS(before) })
	require.NoError(t, httpclient.SetConfiguredTLS(httpclient.TLSSettings{}))

	wantErr := errors.New("stop after setup")
	var duringSetup bool
	err := Run(t.Context(), Options{
		Args:   []string{},
		Stdout: io.Discard,
		Stderr: io.Discard,
		SetupConfig: func(context.Context, *config.LoadResult) error {
			cfg := httpclient.ConfiguredTLS()
			duringSetup = cfg != nil && cfg.InsecureSkipVerify
			return wantErr
		},
	})
	require.ErrorIs(t, err, wantErr)
	assert.True(t, duringSetup, "http.tls was not installed before extension setup")
	assert.Nil(t, httpclient.ConfiguredTLS(), "a rejected generation left its TLS settings installed")
}

// writeTestCA writes the certificate of a throwaway TLS server as a PEM CA
// bundle and returns its path.
func writeTestCA(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// A CA path given as a secret reference must not reach the file system
// unresolved: ${env:...} is resolved for the early install, and a scheme only
// an extension provides leaves the install to the application build.
func TestRunResolvesOutboundTLSPathReferences(t *testing.T) {
	tests := []struct {
		name        string
		caFile      string
		wantInstall bool
	}{
		{"env reference", "${env:GOMODEL_TEST_PRIVATE_CA}", true},
		{"extension scheme", "${vault:pki/ca}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("GOMODEL_TEST_PRIVATE_CA", writeTestCA(t))
			t.Setenv("HTTP_TLS_CA_FILE", tt.caFile)
			before := httpclient.SnapshotTLS()
			t.Cleanup(func() { httpclient.RestoreTLS(before) })
			require.NoError(t, httpclient.SetConfiguredTLS(httpclient.TLSSettings{}))

			wantErr := errors.New("stop after setup")
			var installed bool
			err := Run(t.Context(), Options{
				Args:   []string{},
				Stdout: io.Discard,
				Stderr: io.Discard,
				SetupConfig: func(context.Context, *config.LoadResult) error {
					cfg := httpclient.ConfiguredTLS()
					installed = cfg != nil && cfg.RootCAs != nil
					return wantErr
				},
			})
			require.ErrorIs(t, err, wantErr, "the reference must not fail startup before extension setup")
			assert.Equal(t, tt.wantInstall, installed)
		})
	}
}
