package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadLagoDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		lago := result.Config.Lago
		assert.False(t, lago.Enabled, "the export is opt-in")
		assert.Equal(t, "https://api.getlago.com/api/v1", lago.APIURL)
		assert.Empty(t, lago.APIKey)
		assert.Empty(t, lago.Subscriptions)
		assert.Equal(t, 10000, lago.BufferSize)
	})
}

func TestLoadLagoFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, `
lago:
  enabled: true
  api_url: "https://lago.internal/api/v1/"
  api_key: "lago-key"
  buffer_size: 500
  subscriptions:
    customers/acme/: sub_acme
    /customers/globex: " sub_globex "
`)

		assert.Equal(t, LagoConfig{
			Enabled:    true,
			APIURL:     "https://lago.internal/api/v1",
			APIKey:     "lago-key",
			BufferSize: 500,
			Subscriptions: map[string]string{
				"/customers/acme":   "sub_acme",
				"/customers/globex": "sub_globex",
			},
		}, result.Config.Lago)
	})
}

func TestLoadLagoFromEnvironment(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		t.Setenv("LAGO_ENABLED", "true")
		t.Setenv("LAGO_API_URL", "https://api.eu.getlago.com/api/v1")
		t.Setenv("LAGO_API_KEY", "lago-env-key")
		t.Setenv("LAGO_BUFFER_SIZE", "0")
		t.Setenv("LAGO_SUBSCRIPTIONS", " customers/acme = sub_acme_env , /customers/initech=sub=initech ,")
		result := loadConfigYAML(t, dir, `
lago:
  api_key: "lago-yaml-key"
  subscriptions:
    /customers/acme/: sub_acme_yaml
    /customers/globex: sub_globex
`)

		lago := result.Config.Lago
		assert.True(t, lago.Enabled)
		assert.Equal(t, "https://api.eu.getlago.com/api/v1", lago.APIURL)
		assert.Equal(t, "lago-env-key", lago.APIKey)
		assert.Equal(t, 10000, lago.BufferSize, "a non-positive size falls back to the default")
		assert.Equal(t, map[string]string{
			"/customers/acme":    "sub_acme_env",
			"/customers/globex":  "sub_globex",
			"/customers/initech": "sub=initech",
		}, lago.Subscriptions, "an environment entry replaces the config.yaml entry of the same user path")
	})
}

func TestLoadLagoRejectsInvalidSettings(t *testing.T) {
	const valid = "  api_key: k\n  subscriptions:\n    /acme: sub_acme\n"
	tests := []struct {
		name    string
		yaml    string
		env     map[string]string
		wantErr string
	}{
		{"missing key", "  subscriptions:\n    /acme: sub_acme\n", nil, "lago.api_key (LAGO_API_KEY) is required"},
		{"key from an unset variable", "  api_key: ${LAGO_KEY_NOT_SET}\n  subscriptions:\n    /acme: sub_acme\n", nil, "environment variable that is not set"},
		{"no subscriptions", "  api_key: k\n", nil, "lago.subscriptions (LAGO_SUBSCRIPTIONS) is empty"},
		{"url without scheme", valid + "  api_url: lago.internal/api/v1\n", nil, "lago.api_url (LAGO_API_URL) must be an http or https URL"},
		{"root user path", "  api_key: k\n  subscriptions:\n    /: sub_all\n", nil, "would bill every request to one subscription"},
		{"invalid user path", "  api_key: k\n  subscriptions:\n    /acme/../globex: sub\n", nil, "user path cannot contain"},
		{"empty subscription id", "  api_key: k\n  subscriptions:\n    /acme: \"\"\n", nil, "subscription id is required"},
		{"same user path twice", "  api_key: k\n  subscriptions:\n    /acme: a\n    acme/: b\n", nil, "configured more than once"},
		{"environment entry without id", valid, map[string]string{"LAGO_SUBSCRIPTIONS": "/acme"}, "is not user_path=subscription_id"},
		{"environment entry with invalid path", valid, map[string]string{"LAGO_SUBSCRIPTIONS": "/a:b=sub"}, "invalid value for LAGO_SUBSCRIPTIONS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, "lago:\n  enabled: true\n"+tt.yaml)
				_, err := Load()
				require.ErrorContains(t, err, tt.wantErr)
			})
		})
	}
}

// Settings kept for later must not stop startup while the export is off.
func TestLoadLagoDisabledSkipsValidation(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, "lago:\n  enabled: false\n  api_url: not-a-url\n  subscriptions:\n    /: sub_all\n")
		assert.False(t, result.Config.Lago.Enabled)
	})
}

func TestExportYAMLRedactsLagoAPIKey(t *testing.T) {
	cfg := buildDefaultConfig()
	cfg.Lago.Enabled = true
	cfg.Lago.APIKey = "lago-literal-key"
	cfg.Lago.APIURL = "https://billing:url-pass@lago.internal/api/v1"
	cfg.Lago.Subscriptions = map[string]string{"/customers/acme": "sub_acme"}

	out := exportYAML(t, vaultSecrets(t, nil), cfg, nil, []string{"LAGO_API_KEY=lago-literal-key"})

	assert.Equal(t, "${env:LAGO_API_KEY}", exportedValue(t, out, "lago", "api_key"))
	assert.Equal(t, "sub_acme", exportedValue(t, out, "lago", "subscriptions", "/customers/acme"))
	assert.NotContains(t, out, "lago-literal-key")
	assert.NotContains(t, out, "url-pass")
}
