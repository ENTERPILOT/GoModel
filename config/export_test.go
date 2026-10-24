package config

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// exportYAML resolves cfg with secrets, as Load and ResolveSecrets do, and
// exports it with providers.
func exportYAML(t *testing.T, secrets *Secrets, cfg *Config, providers map[string]RawProviderConfig, environ []string, secretFields ...string) string {
	t.Helper()
	require.NoError(t, secrets.ResolveFields(context.Background(), "", cfg))
	require.NoError(t, secrets.ResolveFields(context.Background(), "providers", &providers))
	out, err := secrets.ExportYAML(ExportDocument{Config: cfg, Providers: providers, SecretFields: secretFields}, environ)
	require.NoError(t, err)
	return string(out)
}

// exportedValue decodes out and returns the string at path.
func exportedValue(t *testing.T, out string, path ...string) string {
	t.Helper()
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &doc))
	var node any = doc
	for _, key := range path {
		if list, ok := node.([]any); ok {
			i, err := strconv.Atoi(key)
			require.NoError(t, err)
			require.Less(t, i, len(list))
			node = list[i]
			continue
		}
		m, ok := node.(map[string]any)
		require.True(t, ok, "%s is not a mapping in:\n%s", key, out)
		node = m[key]
	}
	value, ok := node.(string)
	require.True(t, ok, "%v is %T, not a string", path, node)
	return value
}

func TestExportYAMLWritesSecretsAsReferences(t *testing.T) {
	secrets := vaultSecrets(t, map[string]string{"master": "mk-from-vault", "pg": "pg-pass"})
	cfg := buildDefaultConfig()
	cfg.Server.MasterKey = "${vault:master}"
	cfg.Storage.PostgreSQL.URL = "postgres://app:${vault:pg}@db:5432/gomodel"
	cfg.Storage.EncryptionKey = "enc-from-env"
	cfg.Storage.MongoDB.URL = "mongodb://root:literal-pass@mongo:27017"
	cfg.Cache.Model.ModelList.URL = "https://example.com/models.json"
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"github": {URL: "https://mcp.example.com", Headers: map[string]string{
			"Authorization": "Bearer gh-from-env",
			"X-Team":        "literal-team",
		}},
	}
	environ := []string{"GOMODEL_ENCRYPTION_KEY=enc-from-env", "GITHUB_PAT=gh-from-env"}

	out := exportYAML(t, secrets, cfg, nil, environ)

	assert.Equal(t, "${vault:master}", exportedValue(t, out, "server", "master_key"))
	assert.Equal(t, "postgres://app:${vault:pg}@db:5432/gomodel", exportedValue(t, out, "storage", "postgresql", "url"))
	assert.Equal(t, "${env:GOMODEL_ENCRYPTION_KEY}", exportedValue(t, out, "storage", "encryption_key"))
	assert.Equal(t, "${env:MONGODB_URL}", exportedValue(t, out, "storage", "mongodb", "url"))
	assert.Contains(t, out, "# secret not exported: set MONGODB_URL")
	assert.Equal(t, "https://example.com/models.json", exportedValue(t, out, "cache", "model", "model_list", "url"))
	assert.Equal(t, "Bearer ${env:GITHUB_PAT}", exportedValue(t, out, "mcp", "servers", "github", "headers", "Authorization"))
	assert.Equal(t, "${env:GOMODEL_MCP_SERVERS_GITHUB_HEADERS_X_TEAM}", exportedValue(t, out, "mcp", "servers", "github", "headers", "X-Team"))
	for _, secret := range []string{"mk-from-vault", "pg-pass", "enc-from-env", "literal-pass", "gh-from-env", "literal-team"} {
		assert.NotContains(t, out, secret)
	}
}

func TestExportYAMLProviders(t *testing.T) {
	secrets := vaultSecrets(t, map[string]string{"openai": "sk-vault"})
	providers := map[string]RawProviderConfig{
		"openai": {Type: "openai", APIKey: "${vault:openai}", APIKeys: []string{"sk-literal"}, BaseURL: "https://api.openai.com/v1"},
		"proxied": {
			Type:     "anthropic",
			APIKey:   "sk-ant-env",
			ProxyURL: "socks5://user:proxy-pass@10.0.0.1:1080",
		},
	}

	out := exportYAML(t, secrets, buildDefaultConfig(), providers, []string{"ANTHROPIC_API_KEY=sk-ant-env"})

	assert.Equal(t, "${vault:openai}", exportedValue(t, out, "providers", "openai", "api_key"))
	assert.Contains(t, out, `- "${env:GOMODEL_PROVIDERS_OPENAI_API_KEYS_0}" # secret not exported: set GOMODEL_PROVIDERS_OPENAI_API_KEYS_0`)
	assert.Equal(t, "https://api.openai.com/v1", exportedValue(t, out, "providers", "openai", "base_url"))
	assert.Equal(t, "${env:ANTHROPIC_API_KEY}", exportedValue(t, out, "providers", "proxied", "api_key"))
	assert.Equal(t, "${env:GOMODEL_PROVIDERS_PROXIED_PROXY_URL}", exportedValue(t, out, "providers", "proxied", "proxy_url"))
	for _, secret := range []string{"sk-vault", "sk-literal", "sk-ant-env", "proxy-pass"} {
		assert.NotContains(t, out, secret)
	}
}

func TestExportYAMLUntypedSettings(t *testing.T) {
	var ext yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("address: https://vault.example.com\ntoken: hvs-literal\nrole_id: ${env:VAULT_ROLE}\nnested:\n  clientSecret: cs-literal\n"), &ext))
	cfg := buildDefaultConfig()
	cfg.Extensions = map[string]yaml.Node{"vaults": *ext.Content[0]}
	cfg.Guardrails.Rules = []GuardrailRuleConfig{{
		Name: "scan", Type: "presidio",
		Config: map[string]any{"endpoint": "https://presidio", "subscription": "sub-literal", "max_tokens": 10},
	}}

	out := exportYAML(t, NewSecrets(), cfg, nil, nil, "guardrails.rules[0].config.subscription")

	assert.Equal(t, "https://vault.example.com", exportedValue(t, out, "extensions", "vaults", "address"))
	assert.Equal(t, "${env:GOMODEL_EXTENSIONS_VAULTS_TOKEN}", exportedValue(t, out, "extensions", "vaults", "token"))
	assert.Equal(t, "${env:VAULT_ROLE}", exportedValue(t, out, "extensions", "vaults", "role_id"))
	assert.Equal(t, "${env:GOMODEL_EXTENSIONS_VAULTS_NESTED_CLIENTSECRET}", exportedValue(t, out, "extensions", "vaults", "nested", "clientSecret"))
	assert.Contains(t, out, "endpoint: https://presidio")
	assert.Contains(t, out, "max_tokens: 10")
	assert.NotContains(t, out, "hvs-literal")
	assert.NotContains(t, out, "cs-literal")
	assert.NotContains(t, out, "sub-literal")
}

func TestExportYAMLRedactsSecretsUnderUnknownNames(t *testing.T) {
	// A credential under a setting name no rule recognizes is still caught
	// when it equals a resolved secret or is a URL carrying a password.
	secrets := vaultSecrets(t, map[string]string{"mail": "mail-secret"})
	cfg := buildDefaultConfig()
	cfg.Server.MasterKey = "${vault:mail}"
	cfg.Guardrails.Rules = []GuardrailRuleConfig{{
		Name: "notify", Type: "mailer",
		Config: map[string]any{"smtp_pw": "mail-secret", "relay": "smtp://bot:relay-pass@mail.example.com:587", "server": "smtp://mail.example.com"},
	}}

	out := exportYAML(t, secrets, cfg, nil, nil)

	assert.Equal(t, "${env:GOMODEL_GUARDRAILS_RULES_0_CONFIG_SMTP_PW}", exportedValue(t, out, "guardrails", "rules", "0", "config", "smtp_pw"))
	assert.NotContains(t, out, "mail-secret")
	assert.NotContains(t, out, "relay-pass")
	assert.Contains(t, out, "server: smtp://mail.example.com")
}

func TestExportYAMLSkipsMovedValue(t *testing.T) {
	// A reference resolved at a path whose value has since changed is not
	// written back there: it no longer describes what the path holds.
	secrets := vaultSecrets(t, map[string]string{"a": "key-a"})
	cfg := buildDefaultConfig()
	cfg.Server.MasterKey = "${vault:a}"
	require.NoError(t, secrets.ResolveFields(context.Background(), "", cfg))
	cfg.Server.MasterKey = "moved-literal"

	out, err := secrets.ExportYAML(ExportDocument{Config: cfg}, nil)
	require.NoError(t, err)

	assert.Equal(t, "${env:GOMODEL_MASTER_KEY}", exportedValue(t, string(out), "server", "master_key"))
	assert.NotContains(t, string(out), "moved-literal")
}

func TestExportYAMLHeaderAndNulls(t *testing.T) {
	out, err := NewSecrets().ExportYAML(ExportDocument{Config: buildDefaultConfig(), Header: "Exported for review."}, nil)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(string(out), "# Exported for review.\n"), string(out))
	assert.NotContains(t, string(out), "null")
	assert.NotContains(t, string(out), "\nproviders:")
	assert.NotContains(t, string(out), "virtual_models:", "a nil list is left out")
}

func TestExportYAMLRoundTrips(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_MASTER", "mk-round-trip")
	configYAML := `
server:
  master_key: ${env:GOMODEL_TEST_MASTER}
  body_size_limit: 20M
cache:
  model:
    redis:
      url: redis://cache:6379
      key: gomodel:models
storage:
  type: postgresql
  postgresql:
    url: postgres://app@db/gomodel
budgets:
  user_paths:
    - path: /team
      limits:
        - period: daily
          amount: 5
virtual_models:
  - source: smart
    targets:
      - { model: openai/gpt-4o, weight: 2 }
      - { model: anthropic/claude-sonnet-4-6 }
mcp:
  servers:
    github:
      url: https://mcp.example.com/mcp
      tool_timeout: 45s
workflows:
  refresh_interval: 2m
extensions:
  sample:
    address: https://example.com
`
	var first *LoadResult
	withTempDir(t, func(dir string) {
		require.NoError(t, os.Mkdir("config", 0o755))
		require.NoError(t, os.WriteFile("config/config.yaml", []byte(configYAML), 0o600))
		var err error
		first, err = Load()
		require.NoError(t, err)
	})
	require.NoError(t, first.ResolveSecrets(context.Background()))
	exported, err := first.Secrets.ExportYAML(ExportDocument{Config: first.Config}, os.Environ())
	require.NoError(t, err)
	assert.Contains(t, string(exported), `master_key: "${env:GOMODEL_TEST_MASTER}"`)

	var second *LoadResult
	withTempDir(t, func(dir string) {
		require.NoError(t, os.Mkdir("config", 0o755))
		require.NoError(t, os.WriteFile("config/config.yaml", exported, 0o600))
		second, err = Load()
		require.NoError(t, err, string(exported))
	})
	require.NoError(t, second.ResolveSecrets(context.Background()))
	// Decoded YAML nodes record their line, so extensions are compared as YAML.
	firstExt, err := yaml.Marshal(first.Config.Extensions)
	require.NoError(t, err)
	secondExt, err := yaml.Marshal(second.Config.Extensions)
	require.NoError(t, err)
	assert.Equal(t, string(firstExt), string(secondExt))
	first.Config.Extensions, second.Config.Extensions = nil, nil
	assert.Equal(t, first.Config, second.Config)
}

func TestCredentialKey(t *testing.T) {
	for key, want := range map[string]bool{
		"api_key":        true,
		"apiKey":         true,
		"APIKey":         true,
		"X-Api-Key":      true,
		"Authorization":  true,
		"access_token":   true,
		"client-secret":  true,
		"password":       true,
		"dsn":            true,
		"max_tokens":     false,
		"tokenizer":      false,
		"auth_type":      false,
		"address":        false,
		"key_prefix":     true,
		"monkey":         false,
		"service.secret": true,
	} {
		assert.Equal(t, want, credentialKey(key), key)
	}
}

func TestURLHasLiteralCredential(t *testing.T) {
	for value, want := range map[string]bool{
		"https://api.example.com/v1":            false,
		"redis://cache:6379":                    false,
		"postgres://app@db/gomodel":             false,
		"postgres://app:pass@db/gomodel":        true,
		"postgres://app:${env:PG_PASS}@db/x":    false,
		"postgres://app:x${env:PG_PASS}@db/x":   true,
		"https://example.com/m.json?token=abc":  true,
		"https://example.com/m.json?v=2":        false,
		"https://example.com/?api_key=${env:K}": false,
		"://bad@url":                            true,
	} {
		assert.Equal(t, want, urlHasLiteralCredential(value), value)
	}
}

// credentialFieldName matches a setting name that suggests a credential or a
// value that may embed one.
var credentialFieldName = regexp.MustCompile(`(?i)(key|token|secret|password|credential|url|dsn|header|env|json|host|endpoint)`)

// plainCredentialLookalikes are reviewed settings whose names look like a
// credential but whose values are not secrets.
var plainCredentialLookalikes = map[string]bool{
	"server.user_path_header":          true,
	"server.client_ip_header":          true,
	"cache.model.redis.key":            true,
	"cache.response.simple.redis.key":  true,
	"metrics.endpoint":                 true,
	"failover.rules_json":              true,
	"failover.disabled_models_json":    true,
	"tagging.headers.header":           true,
	"session.headers.header":           true,
	"version_check.url":                true,
	"providers.*.service_account_file": true,
}

// TestExportTagsCoverCredentialFields fails when a string setting whose name
// suggests a credential has no `export` tag and was not reviewed as plain, so
// a new credential field cannot reach an export unredacted by omission.
func TestExportTagsCoverCredentialFields(t *testing.T) {
	var untagged []string
	var visit func(path string, t reflect.Type)
	visit = func(path string, t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			if t.Kind() == reflect.Map {
				path += ".*"
			}
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t == yamlNodeType {
			return
		}
		for field := range t.Fields() {
			name, inline := yamlFieldName(field)
			if !field.IsExported() || name == "-" || field.Tag.Get("yaml") == "-" {
				continue
			}
			fieldPath := path
			if !inline {
				fieldPath = joinSecretPath(path, name)
			}
			if stringish(field.Type) && credentialFieldName.MatchString(name) &&
				field.Tag.Get("export") == "" && !plainCredentialLookalikes[fieldPath] {
				untagged = append(untagged, fieldPath)
			}
			visit(fieldPath, field.Type)
		}
	}
	visit("", reflect.TypeFor[exportRoot]())
	assert.Empty(t, untagged, "tag each field export:\"secret\" or export:\"url\", or add it to plainCredentialLookalikes")
}

// stringish reports whether t holds strings: a string, or a slice or map of
// them.
func stringish(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	return t.Kind() == reflect.String
}
