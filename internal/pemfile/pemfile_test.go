package pemfile

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/tlstest"
)

func TestCheckPath(t *testing.T) {
	key := string(tlstest.NewCA(t, "test CA").Issue(t, "gateway").KeyPEM)
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty", "", false},
		{"file path", "/etc/gomodel/tls/tls.key", false},
		{"PEM contents", key, true},
		{"PEM after other text", "x" + key, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPath("server.tls_key_file", tt.path)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, "server.tls_key_file expects a file path, not PEM contents")
		})
	}
}
