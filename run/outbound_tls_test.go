package run

import (
	"context"
	"errors"
	"io"
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
