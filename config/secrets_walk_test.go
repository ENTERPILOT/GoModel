package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// vaultSecrets returns Secrets with a fake vault scheme serving values.
func vaultSecrets(t *testing.T, values map[string]string) *Secrets {
	t.Helper()
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		value, ok := values[reference]
		if !ok {
			return "", errors.New("not found")
		}
		return value, nil
	})))
	return secrets
}

func TestLoadResultResolveSecretsWalksEveryString(t *testing.T) {
	t.Setenv("GOMODEL_TEST_TOKEN", "gh-token")
	var ext yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("secret: ${vault:unregistered}\n"), &ext))

	result := &LoadResult{
		Secrets: vaultSecrets(t, map[string]string{
			"master":  "mk",
			"pg":      "postgres://u:p@db/gomodel",
			"redis":   "redis://:r@cache:6379",
			"plugin":  "plugin-secret",
			"otel":    "otel-key",
			"openai":  "sk-1",
			"openai2": "sk-2",
			"proxy":   "proxypass",
			"tricky":  "${vault:master}",
		}),
		Config: &Config{
			Server:  ServerConfig{MasterKey: "${vault:master}", EnabledPassthroughProviders: []string{"literal $${not-a-ref}"}},
			Storage: StorageConfig{PostgreSQL: PostgreSQLStorageConfig{URL: "${vault:pg}"}},
			Cache:   CacheConfig{Model: ModelCacheConfig{Redis: &RedisModelConfig{URL: "${vault:redis}"}}},
			MCP: MCPConfig{Servers: map[string]MCPServerConfig{
				"github": {Headers: map[string]string{"Authorization": "Bearer ${env:GOMODEL_TEST_TOKEN}"}},
			}},
			Guardrails: GuardrailsConfig{Rules: []GuardrailRuleConfig{{
				Name:   "scan",
				Config: map[string]any{"api_key": "${vault:plugin}", "nested": map[string]any{"list": []any{"${vault:plugin}", 3}}},
			}}},
			OpenTelemetry: OpenTelemetryConfig{Headers: map[string]string{"x-api-key": "${vault:otel}"}},
			Extensions:    map[string]yaml.Node{"sso": ext},
		},
		RawProviders: map[string]RawProviderConfig{
			"openai": {
				APIKey:   "${vault:openai}",
				APIKeys:  []string{"${vault:openai2}", "${LEGACY_UNSET}"},
				ProxyURL: "http://gomodel:${vault:proxy}@proxy:3128",
				BaseURL:  "${vault:tricky}",
			},
		},
	}

	require.NoError(t, result.ResolveSecrets(t.Context()))

	cfg := result.Config
	assert.Equal(t, "mk", cfg.Server.MasterKey)
	assert.Equal(t, "postgres://u:p@db/gomodel", cfg.Storage.PostgreSQL.URL)
	assert.Equal(t, "redis://:r@cache:6379", cfg.Cache.Model.Redis.URL)
	assert.Equal(t, "Bearer gh-token", cfg.MCP.Servers["github"].Headers["Authorization"])
	assert.Equal(t, "plugin-secret", cfg.Guardrails.Rules[0].Config["api_key"])
	assert.Equal(t, []any{"plugin-secret", 3}, cfg.Guardrails.Rules[0].Config["nested"].(map[string]any)["list"])
	assert.Equal(t, "otel-key", cfg.OpenTelemetry.Headers["x-api-key"])
	assert.Equal(t, []string{"literal ${not-a-ref}"}, cfg.Server.EnabledPassthroughProviders)

	openai := result.RawProviders["openai"]
	assert.Equal(t, "sk-1", openai.APIKey)
	assert.Equal(t, []string{"sk-2", "${LEGACY_UNSET}"}, openai.APIKeys)
	assert.Equal(t, "http://gomodel:proxypass@proxy:3128", openai.ProxyURL)
	assert.Equal(t, "${vault:master}", openai.BaseURL, "a resolved value is never rescanned")

	// Extensions are decoded on demand, not walked.
	stored := cfg.Extensions["sso"]
	assert.Equal(t, "${vault:unregistered}", stored.Content[0].Content[1].Value)

	// A second call is a no-op, so resolved values are not scanned again.
	require.NoError(t, result.ResolveSecrets(t.Context()))
	assert.Equal(t, "${vault:master}", result.RawProviders["openai"].BaseURL)
}

func TestLoadResultResolveSecretsErrorsNameTheField(t *testing.T) {
	tests := []struct {
		name      string
		result    *LoadResult
		wantField string
		wantText  string
	}{
		{
			name:      "provider api key",
			result:    &LoadResult{Config: &Config{}, RawProviders: map[string]RawProviderConfig{"openai": {APIKey: "${vault:prod/openai}"}}},
			wantField: "providers.openai.api_key",
			wantText:  "GoModel Pro vaults",
		},
		{
			name:      "provider api_keys entry",
			result:    &LoadResult{Config: &Config{}, RawProviders: map[string]RawProviderConfig{"openai": {APIKeys: []string{"ok", "${env:GOMODEL_TEST_UNSET}"}}}},
			wantField: "providers.openai.api_keys[1]",
			wantText:  "GOMODEL_TEST_UNSET is not set",
		},
		{
			name:      "master key",
			result:    &LoadResult{Config: &Config{Server: ServerConfig{MasterKey: "${file:relative}"}}},
			wantField: "server.master_key",
			wantText:  "must be absolute",
		},
		{
			name: "mcp header",
			result: &LoadResult{Config: &Config{MCP: MCPConfig{Servers: map[string]MCPServerConfig{
				"github": {Headers: map[string]string{"Authorization": "Bearer ${aws:token}"}},
			}}}},
			wantField: "mcp.servers.github.headers.Authorization",
			wantText:  "unknown secret scheme",
		},
		{
			name: "guardrail plugin config",
			result: &LoadResult{Config: &Config{Guardrails: GuardrailsConfig{Rules: []GuardrailRuleConfig{{
				Config: map[string]any{"auth": map[string]any{"token": "${env:GOMODEL_TEST_UNSET}"}},
			}}}}},
			wantField: "guardrails.rules[0].config.auth.token",
			wantText:  "GOMODEL_TEST_UNSET",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.result.ResolveSecrets(t.Context())
			require.Error(t, err)
			secretErr, ok := errors.AsType[*SecretError](err)
			require.True(t, ok, "error %v is not a *SecretError", err)
			assert.Equal(t, tt.wantField, secretErr.Field)
			assert.Contains(t, err.Error(), tt.wantField+": secret reference ${")
			assert.Contains(t, err.Error(), tt.wantText)
		})
	}
}

func TestLoadResultResolveSecretsNeverLeaksValues(t *testing.T) {
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		if reference == "good" {
			return "sk-very-secret", nil
		}
		return "", errors.New("access denied")
	})))
	result := &LoadResult{
		Secrets:      secrets,
		Config:       &Config{},
		RawProviders: map[string]RawProviderConfig{"openai": {APIKey: "${vault:good}", ProxyURL: "http://u:${vault:good}@h${vault:bad}"}},
	}
	err := result.ResolveSecrets(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-very-secret")
	assert.Contains(t, err.Error(), "providers.openai.proxy_url: secret reference ${vault:...}: access denied")
}

func TestLoadResultResolveSecretsHandlesNil(t *testing.T) {
	var nilResult *LoadResult
	require.NoError(t, nilResult.ResolveSecrets(t.Context()))
	require.NoError(t, (&LoadResult{}).ResolveSecrets(t.Context()))
}

func TestLoadLeavesSecretReferencesForTheResolutionPass(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_LEGACY", "legacy")
	t.Setenv("GOMODEL_TEST_TOKEN", "tok")
	withTempDir(t, func(dir string) {
		keyFile := filepath.Join(dir, "master-key")
		require.NoError(t, os.WriteFile(keyFile, []byte("mk-from-file\n"), 0o600))
		result := loadConfigYAML(t, dir, `
server:
  master_key: ${file:`+keyFile+`}
providers:
  openai:
    type: openai
    api_key: ${env:GOMODEL_TEST_TOKEN}
    base_url: https://${GOMODEL_TEST_LEGACY}.example.com/$${literal}
`)
		require.NotNil(t, result.Secrets)
		assert.Equal(t, "${file:"+keyFile+"}", result.Config.Server.MasterKey)

		require.NoError(t, result.ResolveSecrets(t.Context()))
		assert.Equal(t, "mk-from-file", result.Config.Server.MasterKey)
		assert.Equal(t, "tok", result.RawProviders["openai"].APIKey)
		assert.Equal(t, "https://legacy.example.com/${literal}", result.RawProviders["openai"].BaseURL)
	})
}

func TestLoadResultResolveSecretsResolvesEnvOverrides(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_TOKEN", "tok")
	t.Setenv("GOMODEL_MASTER_KEY", "${env:GOMODEL_TEST_TOKEN}")
	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.NoError(t, result.ResolveSecrets(t.Context()))
		assert.Equal(t, "tok", result.Config.Server.MasterKey)
	})
}

func TestDecodeExtensionResolvesSecretReferences(t *testing.T) {
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(`
client_secret: ${vault:sso}
token_file: ${vault:multiline}
scopes: [openid, "${vault:scope}"]
literal: $${vault:sso}
`), &node))
	result := &LoadResult{
		Config:  &Config{Extensions: map[string]yaml.Node{"sso": node}},
		Secrets: vaultSecrets(t, map[string]string{"sso": "shh: \"quoted\"", "multiline": "line1\nline2", "scope": "${vault:sso}"}),
	}

	var got struct {
		ClientSecret string   `yaml:"client_secret"`
		TokenFile    string   `yaml:"token_file"`
		Scopes       []string `yaml:"scopes"`
		Literal      string   `yaml:"literal"`
	}
	found, err := result.DecodeExtension("sso", &got)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, `shh: "quoted"`, got.ClientSecret)
	assert.Equal(t, "line1\nline2", got.TokenFile)
	assert.Equal(t, []string{"openid", "${vault:sso}"}, got.Scopes)
	assert.Equal(t, "${vault:sso}", got.Literal)

	// The stored section keeps its references for the next decode.
	stored := result.Config.Extensions["sso"]
	assert.Equal(t, "${vault:sso}", stored.Content[0].Content[1].Value)
}

func TestDecodeExtensionSecretErrors(t *testing.T) {
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("stores:\n  prod:\n    token: ${vault:x}\n"), &node))
	result := &LoadResult{Config: &Config{Extensions: map[string]yaml.Node{"vaults": node}}}

	var got map[string]any
	_, err := result.DecodeExtension("vaults", &got)
	require.Error(t, err)
	secretErr, ok := errors.AsType[*SecretError](err)
	require.True(t, ok)
	assert.Equal(t, "extensions.vaults.stores.prod.token", secretErr.Field)
	assert.ErrorIs(t, err, ErrUnknownSecretScheme)
}
