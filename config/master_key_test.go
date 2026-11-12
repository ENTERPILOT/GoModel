package config

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	masterKeyPlaceholderError = "is a placeholder from the GoModel templates or documentation"
	masterKeyWeakWarning      = "GOMODEL_MASTER_KEY is short"
)

func captureMasterKeyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &logs
}

// The example keys in the templates and the documentation are public, so a
// gateway must not start with one. A short key only draws a warning, logged
// when the gateway resolves its secrets: existing deployments keep starting.
func TestMasterKeyCheck(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		wantErr     bool
		wantWarning bool
	}{
		{name: "env template placeholder", key: "your-secret-key-here", wantErr: true},
		{name: "example config placeholder", key: "your-secret-key", wantErr: true},
		{name: "configuration docs placeholder", key: "my-secret-key", wantErr: true},
		{name: "documentation placeholder", key: "change-me", wantErr: true},
		{name: "placeholder in another case", key: "Change-Me", wantErr: true},
		{name: "short key", key: "short-key", wantWarning: true},
		{name: "key just under the minimum", key: "123456789012345", wantWarning: true},
		{name: "key at the minimum", key: "1234567890123456"},
		{name: "generated key", key: "0b6f0c0f9f1c4f6f8d1e6a3a5c7e9b1d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e"},
		{name: "no key"},
	}
	sources := []struct {
		name string
		// set configures the key and reports whether it is a secret
		// reference, which Load cannot judge before it resolves.
		set func(t *testing.T, dir, key string) (reference bool)
	}{
		{name: "environment", set: func(t *testing.T, _, key string) bool {
			t.Setenv("GOMODEL_MASTER_KEY", key)
			return false
		}},
		{name: "config.yaml", set: func(t *testing.T, dir, key string) bool {
			writeConfigYAML(t, dir, "server:\n  master_key: \""+key+"\"\n")
			return false
		}},
		{name: "secret reference", set: func(t *testing.T, dir, key string) bool {
			t.Setenv("GOMODEL_TEST_MASTER_KEY", key)
			writeConfigYAML(t, dir, "server:\n  master_key: ${env:GOMODEL_TEST_MASTER_KEY}\n")
			return true
		}},
	}
	for _, source := range sources {
		for _, tt := range tests {
			if source.name == "secret reference" && tt.key == "" {
				continue // an env reference to an empty variable does not resolve
			}
			t.Run(source.name+"/"+tt.name, func(t *testing.T) {
				clearAllConfigEnvVars(t)
				logs := captureMasterKeyLogs(t)
				withTempDir(t, func(dir string) {
					reference := source.set(t, dir, tt.key)

					result, err := Load()
					if tt.wantErr && !reference {
						require.Error(t, err)
						assert.Contains(t, err.Error(), masterKeyPlaceholderError)
						assert.Contains(t, err.Error(), "openssl rand -hex 32")
						return
					}
					require.NoError(t, err)
					assert.NotContains(t, logs.String(), masterKeyWeakWarning, "Load alone does not warn")

					err = result.ResolveSecrets(context.Background())
					if tt.wantErr {
						require.Error(t, err)
						assert.Contains(t, err.Error(), masterKeyPlaceholderError)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, tt.key, result.Config.Server.MasterKey)
					if tt.wantWarning {
						assert.Contains(t, logs.String(), masterKeyWeakWarning)
						assert.NotContains(t, logs.String(), tt.key)
					} else {
						assert.NotContains(t, logs.String(), masterKeyWeakWarning)
					}
				})
			})
		}
	}
}

// A disabled master key is forgotten at load, so its value is never judged.
func TestMasterKeyCheckSkippedWhenDisabled(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(string) {
		t.Setenv("GOMODEL_MASTER_KEY", "change-me")
		t.Setenv("MASTER_KEY_DISABLED", "true")

		result, err := Load()
		require.NoError(t, err)
		require.NoError(t, result.ResolveSecrets(context.Background()))
		assert.Empty(t, result.Config.Server.MasterKey)
	})
}
