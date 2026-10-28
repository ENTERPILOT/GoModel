package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/tlstest"
)

// A generation is built only when its listener certificate loads, so a typo
// fails startup and a reload with a broken pair keeps the running generation.
func TestServerTLSConfig(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, dir)
	missing := filepath.Join(dir, "missing.crt")

	tests := []struct {
		name    string
		server  config.ServerConfig
		wantTLS bool
		wantErr bool
	}{
		{name: "unset serves plain HTTP"},
		{name: "valid pair", server: config.ServerConfig{TLSCertFile: certFile, TLSKeyFile: keyFile}, wantTLS: true},
		{name: "missing certificate", server: config.ServerConfig{TLSCertFile: missing, TLSKeyFile: keyFile}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := serverTLSConfig(tt.server)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantTLS, got != nil)
		})
	}
}
