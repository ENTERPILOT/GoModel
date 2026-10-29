package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/platformdir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pid file follows the database instead of scattering GoModel's state
// across the filesystem: a Docker image with /app/data keeps both
// project-local, and a binary install started from an arbitrary working
// directory keeps both in the per-user data directory.
func TestDefaultPIDFilePath(t *testing.T) {
	platformDataDir, err := platformdir.DataDir()
	require.NoError(t, err)

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "data directory exists keeps the project-local path",
			setup: func(t *testing.T, dir string) {
				err := os.Mkdir(filepath.Join(dir, "data"), 0o755)
				require.NoError(t, err)
			},
			want: LegacyPIDFilePath,
		},
		{
			name:  "no data directory uses the platform path",
			setup: func(t *testing.T, dir string) {},
			want:  filepath.Join(platformDataDir, "gomodel.pid"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			t.Chdir(dir)
			got := DefaultPIDFilePath()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPIDFilePathResolution(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		configYAML string
		want       string
	}{
		{
			name: "env var wins",
			env:  "/var/run/gomodel/custom.pid",
			want: "/var/run/gomodel/custom.pid",
		},
		{
			// Empty env vars are "unset" everywhere in this config, so PID_FILE=
			// keeps the default rather than disabling the pid file. Asserted so
			// the documented way to disable it stays the config file.
			name: "empty env var keeps the default",
			env:  "",
			want: DefaultPIDFilePath(),
		},
		{
			name:       "empty config value writes no pid file",
			configYAML: "server:\n  pid_file: \"\"\n",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("PID_FILE", tt.env)
			if tt.configYAML != "" {
				err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tt.configYAML), 0o600)
				require.NoError(t, err)
			}

			result, err := Load()
			require.NoError(t, err)
			got := result.Config.Server.PIDFile
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadServerTLSSettings(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		configYAML string
		wantErr    string
		want       [3]string // cert, key, client CA
		wantScheme string
	}{
		{
			name:       "unset serves plain HTTP",
			wantScheme: "http",
		},
		{
			name: "env vars set the files",
			env: map[string]string{
				"SERVER_TLS_CERT_FILE":      "/etc/tls/tls.crt",
				"SERVER_TLS_KEY_FILE":       "/etc/tls/tls.key",
				"SERVER_TLS_CLIENT_CA_FILE": "/etc/tls/ca.crt",
			},
			want:       [3]string{"/etc/tls/tls.crt", "/etc/tls/tls.key", "/etc/tls/ca.crt"},
			wantScheme: "https",
		},
		{
			name:       "env vars override YAML",
			env:        map[string]string{"SERVER_TLS_CERT_FILE": "/env/tls.crt"},
			configYAML: "server:\n  tls_cert_file: /yaml/tls.crt\n  tls_key_file: /yaml/tls.key\n",
			want:       [3]string{"/env/tls.crt", "/yaml/tls.key", ""},
			wantScheme: "https",
		},
		{
			name:    "certificate without key",
			env:     map[string]string{"SERVER_TLS_CERT_FILE": "/etc/tls/tls.crt"},
			wantErr: "must be set together",
		},
		{
			name:       "key without certificate",
			configYAML: "server:\n  tls_key_file: /etc/tls/tls.key\n",
			wantErr:    "must be set together",
		},
		{
			name:    "client CA without a certificate",
			env:     map[string]string{"SERVER_TLS_CLIENT_CA_FILE": "/etc/tls/ca.crt"},
			wantErr: "tls_client_ca_file requires",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			withTempDir(t, func(dir string) {
				if tt.configYAML != "" {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tt.configYAML), 0o600))
				}
				result, err := Load()
				if tt.wantErr != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.wantErr)
					return
				}
				require.NoError(t, err)
				server := result.Config.Server
				assert.Equal(t, tt.want, [3]string{server.TLSCertFile, server.TLSKeyFile, server.TLSClientCAFile})
				assert.Equal(t, tt.wantScheme, server.Scheme())
			})
		})
	}
}

// The paths accept secret references like every other setting; they resolve
// to the path the listener then reads.
func TestServerTLSPathReferencesResolve(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(dir string) {
		pathFile := filepath.Join(dir, "key-path")
		require.NoError(t, os.WriteFile(pathFile, []byte("/etc/tls/tls.key\n"), 0o600))
		t.Setenv("GOMODEL_TEST_TLS_CERT", "/etc/tls/tls.crt")
		yaml := "server:\n  tls_cert_file: ${env:GOMODEL_TEST_TLS_CERT}\n  tls_key_file: ${file:" + pathFile + "}\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))

		result, err := Load()
		require.NoError(t, err)
		require.NoError(t, result.ResolveSecrets(t.Context()))
		assert.Equal(t, "/etc/tls/tls.crt", result.Config.Server.TLSCertFile)
		assert.Equal(t, "/etc/tls/tls.key", result.Config.Server.TLSKeyFile)
	})
}

// A reference that resolves to empty must fail instead of quietly serving
// plain HTTP or half a pair. The built-in schemes already reject empty values;
// a scheme an extension registers, such as a vault, may not.
func TestServerTLSReferencesResolvingEmptyFail(t *testing.T) {
	tests := []struct {
		name    string
		cert    string
		wantErr string
	}{
		{name: "both empty", cert: "${blank:cert}", wantErr: "resolved to empty values"},
		{name: "key empty", cert: "/etc/tls/tls.crt", wantErr: "must be set together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(dir string) {
				yaml := "server:\n  tls_cert_file: " + tt.cert + "\n  tls_key_file: ${blank:key}\n"
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))

				result, err := Load()
				require.NoError(t, err)
				require.NoError(t, result.Secrets.Register("blank", SecretResolverFunc(func(context.Context, string) (string, error) {
					return "", nil
				})))
				err = result.ResolveSecrets(t.Context())
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			})
		})
	}
}
