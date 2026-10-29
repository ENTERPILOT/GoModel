package pemfile

import (
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestAppendCertificates(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	other := tlstest.NewCA(t, "other CA")
	join := func(parts ...string) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name    string
		data    []byte
		want    int
		wantErr string
	}{
		{"one certificate", ca.PEM, 1, ""},
		{"bundle", join(string(ca.PEM), string(other.PEM)), 2, ""},
		{"quoted marker in a comment", join("# paste each CA as -----BEGIN CERTIFICATE----- below\n", string(ca.PEM)), 1, ""},
		{"other block types are skipped", join(string(ca.PEM), "-----BEGIN X509 CRL-----\nAAAA\n-----END X509 CRL-----\n"), 1, ""},
		{"not PEM", []byte("not a certificate"), 0, "contains no PEM certificates"},
		{"certificate that does not parse", join(string(ca.PEM), "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), 0, "certificate 2"},
		{"bad base64", join("-----BEGIN CERTIFICATE-----\n!!!\n-----END CERTIFICATE-----\n", string(ca.PEM)), 0, "1 of 2 PEM blocks are malformed"},
		{"BEGIN line without dashes", join(string(ca.PEM), "-----BEGIN CERTIFICATE\nAAAA\n-----END CERTIFICATE-----\n"), 0, "1 of 2 PEM blocks are malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := x509.NewCertPool()
			err := AppendCertificates(pool, tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, pool.Subjects(), tt.want) //nolint:staticcheck // Subjects is fine for a pool built here.
		})
	}
}
