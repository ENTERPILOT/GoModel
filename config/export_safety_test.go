package config

import (
	"context"
	"math/rand/v2"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// loadYAML loads data as config/config.yaml, resolving secrets.
func loadYAML(t *testing.T, data []byte) *LoadResult {
	t.Helper()
	var result *LoadResult
	withTempDir(t, func(string) {
		require.NoError(t, os.Mkdir("config", 0o755))
		require.NoError(t, os.WriteFile("config/config.yaml", data, 0o600))
		var err error
		result, err = Load()
		require.NoError(t, err, string(data))
	})
	require.NoError(t, result.ResolveSecrets(context.Background()))
	return result
}

func TestExportYAMLWritesTextAsText(t *testing.T) {
	// Text from the dashboard, which a user-path scoped key can write, must
	// load back as the same text: "${env:...}" must not become a reference
	// reading the master key, nor "$NAME" expand.
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_MK", "mk-do-not-leak")
	texts := []string{
		"${env:GOMODEL_TEST_MK}",
		"${file:/etc/hosts}",
		"$GOMODEL_TEST_MK and ${GOMODEL_TEST_MK} and ${GOMODEL_TEST_MK:-x}",
		"costs $5, $$, $${kept}, trailing $",
		"quote \" backslash \\ tab \t newline \n bell \a",
	}
	cfg := buildDefaultConfig()
	for i, text := range texts {
		cfg.Users = append(cfg.Users, UserConfig{Path: "/u" + string(rune('a'+i)), Description: text})
	}
	cfg.OpenTelemetry.ResourceAttributes = map[string]string{"team$GOMODEL_TEST_MK": "a"}

	out, err := (&LoadResult{Secrets: NewSecrets()}).ExportYAML(ExportDocument{Config: cfg}, os.Environ())
	require.NoError(t, err)
	assert.NotContains(t, string(out), "mk-do-not-leak")

	second := loadYAML(t, out)
	require.Len(t, second.Config.Users, len(texts))
	for i, text := range texts {
		assert.Equal(t, text, second.Config.Users[i].Description)
	}
	assert.Equal(t, map[string]string{"team$GOMODEL_TEST_MK": "a"}, second.Config.OpenTelemetry.ResourceAttributes)
}

func TestExportYAMLKeepsExpandedSettingsAsWritten(t *testing.T) {
	// A setting environment expansion rewrote is written as in config.yaml,
	// so it keeps reading its variable, or, holding a secret, as a reference.
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_TOOL_KEY", "tool-key-do-not-leak")
	t.Setenv("GOMODEL_TEST_TEAM", "platform")
	first := loadYAML(t, []byte(`
mcp:
  servers:
    tools:
      transport: stdio
      command: npx
      args: ["-y", "tools-server", "--api-key=${GOMODEL_TEST_TOOL_KEY}", "--token", "$GOMODEL_TEST_TOOL_KEY", "--team=${GOMODEL_TEST_TEAM}"]
      description: Team ${GOMODEL_TEST_TEAM}
opentelemetry:
  resource_attributes:
    team: ${GOMODEL_TEST_TEAM}
    api_key: ${GOMODEL_TEST_TOOL_KEY}
`))

	out, err := first.ExportYAML(ExportDocument{Config: first.Config}, os.Environ())
	require.NoError(t, err)
	body := string(out)

	assert.NotContains(t, body, "tool-key-do-not-leak")
	assert.Contains(t, body, `- "--api-key=${env:GOMODEL_TEST_TOOL_KEY}"`)
	assert.Contains(t, body, `- "${env:GOMODEL_TEST_TOOL_KEY}"`)
	assert.Contains(t, body, `- "--team=${GOMODEL_TEST_TEAM}"`)
	assert.Contains(t, body, `description: Team ${GOMODEL_TEST_TEAM}`)
	assert.Contains(t, body, `team: ${GOMODEL_TEST_TEAM}`)
	assert.Contains(t, body, `api_key: "${env:GOMODEL_TEST_TOOL_KEY}"`)

	second := loadYAML(t, out)
	assert.Equal(t, first.Config.MCP, second.Config.MCP)
	assert.Equal(t, first.Config.OpenTelemetry, second.Config.OpenTelemetry)
}

func TestExportYAMLRedactsCredentialsOutsideSecretFields(t *testing.T) {
	var ext yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("auth: ext-auth\ncookie: ext-cookie\npass: ext-pass\nregion: eu\n"), &ext))
	cfg := buildDefaultConfig()
	cfg.Storage.PostgreSQL.URL = "host=db user=app password=pg-keyword dbname=gomodel"
	cfg.Storage.MongoDB.URL = "mongodb://mongo-token@mongo:27017"
	cfg.Cache.Model.ModelList.URL = "https://bucket.example.com/models.json?X-Amz-Signature=s3-signature"
	cfg.OpenTelemetry.ResourceAttributes = map[string]string{"api_key": "otel-attr", "team": "platform"}
	cfg.Extensions = map[string]yaml.Node{"sample": *ext.Content[0]}
	cfg.Guardrails.Rules = []GuardrailRuleConfig{{Name: "scan", Type: "custom", Config: map[string]any{"token": "rule-token", "mode": "block"}}}
	cfg.MCP.Servers = map[string]MCPServerConfig{"tools": {
		Transport: "stdio", Command: "npx",
		Args: []string{"-y", "server", "--token", "arg-token", "--api-key=arg-key", "-H", "Authorization: Bearer arg-bearer", "GITHUB_TOKEN=arg-env"},
	}}

	out := exportYAML(t, NewSecrets(), cfg, nil, nil)

	for _, secret := range []string{"pg-keyword", "mongo-token", "s3-signature", "otel-attr", "ext-auth", "ext-cookie", "ext-pass", "rule-token", "arg-token", "arg-key", "arg-bearer", "arg-env"} {
		assert.NotContains(t, out, secret)
	}
	assert.Equal(t, "platform", exportedValue(t, out, "opentelemetry", "resource_attributes", "team"))
	assert.Equal(t, "eu", exportedValue(t, out, "extensions", "sample", "region"))
	assert.Equal(t, "block", exportedValue(t, out, "guardrails", "rules", "0", "config", "mode"))
	assert.Equal(t, "--api-key=${env:GOMODEL_MCP_SERVERS_TOOLS_ARGS_4}", exportedValue(t, out, "mcp", "servers", "tools", "args", "4"))
	assert.Equal(t, "Authorization: Bearer ${env:GOMODEL_MCP_SERVERS_TOOLS_ARGS_6}", exportedValue(t, out, "mcp", "servers", "tools", "args", "6"))
	assert.Equal(t, "GITHUB_TOKEN=${env:GOMODEL_MCP_SERVERS_TOOLS_ARGS_7}", exportedValue(t, out, "mcp", "servers", "tools", "args", "7"))
}

func TestExportYAMLKeepsStoredReferences(t *testing.T) {
	// A reference stored with a dashboard row the gateway resolves is kept; one
	// anywhere else is text.
	providers := map[string]RawProviderConfig{"groq": {Type: "groq", APIKey: "${vault:groq}", ProxyURL: "http://u:${env:PROXY_PASS}@proxy:8080"}}
	cfg := buildDefaultConfig()
	cfg.Users = []UserConfig{{Path: "/team", Description: "${vault:groq}"}}

	out, err := (&LoadResult{Secrets: NewSecrets()}).ExportYAML(ExportDocument{
		Config:     cfg,
		Providers:  providers,
		References: []string{"providers.groq.api_key", "providers.groq.proxy_url"},
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, "${vault:groq}", exportedValue(t, string(out), "providers", "groq", "api_key"))
	assert.Equal(t, "http://u:${env:PROXY_PASS}@proxy:8080", exportedValue(t, string(out), "providers", "groq", "proxy_url"))
	assert.Equal(t, "$${vault:groq}", exportedValue(t, string(out), "users", "0", "description"))
}

// TestExportYAMLChecksEveryString fails when the export writes a string
// setting it did not check: every string reachable from the configuration,
// in structs, slices, maps, free-form values, and extension sections, is set
// to a secret, and none may reach the document.
func TestExportYAMLChecksEveryString(t *testing.T) {
	for name, tc := range map[string]struct {
		value, secret string
	}{
		"resolved secret":     {value: "canary-secret-do-not-leak", secret: "canary-secret-do-not-leak"},
		"URL with a password": {value: "postgres://app:canary-pass@db/gomodel", secret: "canary-pass"},
		"DSN with a password": {value: "host=db password=canary-pass", secret: "canary-pass"},
		"multi-line secret":   {value: "\n canary-multi-line", secret: "canary-multi-line"},
	} {
		t.Run(name, func(t *testing.T) {
			secrets := vaultSecrets(t, map[string]string{"canary": tc.value})
			_, err := secrets.resolveField(context.Background(), "server.master_key", "${vault:canary}")
			require.NoError(t, err)

			cfg := &Config{}
			fillStrings(reflect.ValueOf(cfg).Elem(), tc.value)
			providers := map[string]RawProviderConfig{}
			fillStrings(reflect.ValueOf(&providers).Elem(), tc.value)

			out, err := (&LoadResult{Secrets: secrets}).ExportYAML(ExportDocument{Config: cfg, Providers: providers}, nil)
			require.NoError(t, err)
			var leaked []string
			for line := range strings.SplitSeq(string(out), "\n") {
				if strings.Contains(line, tc.secret) {
					leaked = append(leaked, strings.TrimSpace(line))
				}
			}
			assert.Empty(t, leaked)
			assert.Contains(t, string(out), "secret not exported")
			assert.NotContains(t, string(out), "gomodelexport", "a placeholder reached the document")
		})
	}
}

// fillStrings sets every string reachable from v to value, adding one entry
// to each slice and map and allocating each pointer.
func fillStrings(v reflect.Value, value string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(value)
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem(), value)
	case reflect.Struct:
		if v.Type() == yamlNodeType {
			v.Set(reflect.ValueOf(yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "setting"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
			}}))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillStrings(v.Field(i), value)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillStrings(v.Index(0), value)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		if key.Kind() == reflect.String {
			key.SetString("entry")
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		fillStrings(elem, value)
		v.SetMapIndex(key, elem)
	case reflect.Interface:
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf(map[string]any{"setting": value}))
		}
	}
}

func TestExportYAMLRedactsUntypedNonStrings(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_GH_TOKEN", "gh-env-do-not-leak")
	first := loadYAML(t, []byte(`
extensions:
  sample:
    password: 31415926
    token: !!binary Z2gtYmluYXJ5LWRvLW5vdC1sZWFr
    api_key: !secret "tagged-do-not-leak"
    note: !mytag "$GOMODEL_TEST_GH_TOKEN"
    count: 3
`))
	out, err := first.ExportYAML(ExportDocument{Config: first.Config}, nil)
	require.NoError(t, err)
	body := string(out)

	for _, secret := range []string{"31415926", "Z2gtYmluYXJ5", "tagged-do-not-leak", "gh-env-do-not-leak"} {
		assert.NotContains(t, body, secret)
	}
	assert.Contains(t, body, "count: 3")
}

func TestExportYAMLRedactsArgumentForms(t *testing.T) {
	cfg := buildDefaultConfig()
	cfg.MCP.Servers = map[string]MCPServerConfig{"remote": {
		Transport: "stdio", Command: "npx",
		Args: []string{
			"mcp-remote", "https://mcp.example.com",
			"--header=Authorization: Bearer hdr-do-not-leak",
			"Authorization", "Bearer split-do-not-leak",
			`--config={"apiKey":"json-do-not-leak"}`,
			`{"token": "bare-json-do-not-leak"}`,
			`--config={"region":"us","apiKey":"later-json-do-not-leak"}`,
			"--pat", "pat-do-not-leak",
		},
	}}

	out := exportYAML(t, NewSecrets(), cfg, nil, nil)

	for _, secret := range []string{"hdr-do-not-leak", "split-do-not-leak", "json-do-not-leak", "bare-json-do-not-leak", "later-json-do-not-leak", "pat-do-not-leak"} {
		assert.NotContains(t, out, secret)
	}
	assert.Equal(t, "mcp-remote", exportedValue(t, out, "mcp", "servers", "remote", "args", "0"))
	assert.Equal(t, "--header=Authorization: Bearer ${env:GOMODEL_MCP_SERVERS_REMOTE_ARGS_2}", exportedValue(t, out, "mcp", "servers", "remote", "args", "2"))
	// The variable holds the whole JSON, so the argument stays valid JSON.
	assert.Equal(t, "--config=${env:GOMODEL_MCP_SERVERS_REMOTE_ARGS_5}", exportedValue(t, out, "mcp", "servers", "remote", "args", "5"))
	assert.Equal(t, "${env:GOMODEL_MCP_SERVERS_REMOTE_ARGS_6}", exportedValue(t, out, "mcp", "servers", "remote", "args", "6"))
	assert.Equal(t, "--config=${env:GOMODEL_MCP_SERVERS_REMOTE_ARGS_7}", exportedValue(t, out, "mcp", "servers", "remote", "args", "7"))
}

func TestExportYAMLRedactsCredentialsInText(t *testing.T) {
	cfg := buildDefaultConfig()
	cfg.Cache.Model.ModelList.URL = "postgres://db/gomodel?sslpassword=ssl-do-not-leak"
	cfg.OpenTelemetry.ResourceAttributes = map[string]string{"note": "Bearer bearer-do-not-leak", "spaced": "Bearer   spaced-do-not-leak", "plan": "Bearer of good news"}
	cfg.Users = []UserConfig{{Path: "/a", Description: "token=assigned-do-not-leak"}, {Path: "/b", Description: "max_tokens=5"}}

	out := exportYAML(t, NewSecrets(), cfg, nil, nil)

	for _, secret := range []string{"ssl-do-not-leak", "bearer-do-not-leak", "spaced-do-not-leak", "assigned-do-not-leak"} {
		assert.NotContains(t, out, secret)
	}
	assert.Equal(t, "Bearer of good news", exportedValue(t, out, "opentelemetry", "resource_attributes", "plan"))
	assert.Equal(t, "max_tokens=5", exportedValue(t, out, "users", "1", "description"))
}

func TestExportYAMLOnlyCountsSecretsResolvedIntoSecretSettings(t *testing.T) {
	// A tenant copying what a reference resolved to into a description must
	// not make the export ask for a variable, unless it copied a secret.
	secrets := vaultSecrets(t, map[string]string{"url": "http://127.0.0.1:1/v1", "mk": "mk-resolved"})
	cfg := buildDefaultConfig()
	cfg.Server.MasterKey = "${vault:mk}"
	cfg.Users = []UserConfig{{Path: "/a", Description: "http://127.0.0.1:1/v1"}, {Path: "/b", Description: "mk-resolved"}}
	providers := map[string]RawProviderConfig{"ollama": {Type: "ollama", BaseURL: "${vault:url}"}}

	out := exportYAML(t, secrets, cfg, providers, nil)

	assert.Equal(t, "http://127.0.0.1:1/v1", exportedValue(t, out, "users", "0", "description"))
	assert.Equal(t, "${vault:url}", exportedValue(t, out, "providers", "ollama", "base_url"))
	assert.NotContains(t, out, "mk-resolved")
}

func TestExportYAMLRedactsCopiesOfSecrets(t *testing.T) {
	// Supplied by a variable as is, a secret has no reference recording it.
	cfg := buildDefaultConfig()
	cfg.Storage.EncryptionKey = "kek-from-env-do-not-leak"
	cfg.Users = []UserConfig{
		{Path: "/a", Description: "kek-from-env-do-not-leak"},
		{Path: "/b", Description: "the key is kek-from-env-do-not-leak"},
		{Path: "/c", Description: "short"},
	}
	cfg.Server.MasterKey = "short"

	out := exportYAML(t, NewSecrets(), cfg, nil, []string{"GOMODEL_ENCRYPTION_KEY=kek-from-env-do-not-leak"})

	assert.NotContains(t, out, "kek-from-env-do-not-leak")
	assert.Equal(t, "${env:GOMODEL_ENCRYPTION_KEY}", exportedValue(t, out, "users", "0", "description"))
	assert.Equal(t, "short", exportedValue(t, out, "users", "2", "description"), "a short secret is not matched in text")
}

func TestExportYAMLLoadsBackEdgeCases(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_ALIAS_MK", "mk-alias-do-not-leak")
	longKey := strings.Repeat("K", 1100) + "$"
	first := loadYAML(t, []byte(`
server:
  master_key: &mk ${env:GOMODEL_TEST_ALIAS_MK}
extensions:
  zeta: &shared
    region: eu
  alpha: *shared
  copy:
    value: *mk
`))
	cfg := first.Config
	cfg.Users = []UserConfig{{Path: "/a", Description: "\nhello"}, {Path: "/b", Description: " \n indented"}}
	cfg.OpenTelemetry.ResourceAttributes = map[string]string{longKey: "v"}

	out, err := first.ExportYAML(ExportDocument{Config: cfg}, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "mk-alias-do-not-leak")
	second := loadYAML(t, out)

	assert.Equal(t, "\nhello", second.Config.Users[0].Description)
	assert.Equal(t, " \n indented", second.Config.Users[1].Description)
	assert.Equal(t, map[string]string{longKey: "v"}, second.Config.OpenTelemetry.ResourceAttributes)
	var alpha struct {
		Region string `yaml:"region"`
	}
	_, err = second.DecodeExtension("alpha", &alpha)
	require.NoError(t, err)
	assert.Equal(t, "eu", alpha.Region)
}

func TestExportYAMLSuggestsDistinctVariables(t *testing.T) {
	providers := map[string]RawProviderConfig{
		"a.b": {Type: "openai", APIKey: "sk-one"},
		"a_b": {Type: "openai", APIKey: "sk-two"},
	}

	out := exportYAML(t, NewSecrets(), buildDefaultConfig(), providers, nil)

	assert.NotEqual(t,
		exportedValue(t, out, "providers", "a.b", "api_key"),
		exportedValue(t, out, "providers", "a_b", "api_key"))
}

func TestExportYAMLRoundTripsRandomText(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_FUZZ", "fuzz-do-not-leak")
	alphabet := []string{"a", "Z", "0", " ", "\n", "\t", "$", "{", "}", "${", "$$", "env:", "GOMODEL_TEST_FUZZ", ":", "#", "'", `"`, `\`, "-", "?", "&", "*", "!", "|", ">", "%", "@", " ", "é", "\x7f"}
	rng := rand.New(rand.NewPCG(1, 2))
	text := func() string {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	attributes := map[string]string{}
	for range 300 {
		attributes["k"+text()] = text()
	}
	cfg := buildDefaultConfig()
	cfg.OpenTelemetry.ResourceAttributes = attributes

	out, err := (&LoadResult{Secrets: NewSecrets()}).ExportYAML(ExportDocument{Config: cfg}, nil)
	require.NoError(t, err)
	second := loadYAML(t, out)

	assert.NotContains(t, string(out), "fuzz-do-not-leak")
	for key, value := range attributes {
		got, ok := second.Config.OpenTelemetry.ResourceAttributes[key]
		if !assert.True(t, ok, "key %q", key) {
			continue
		}
		if !strings.Contains(got, "${env:") {
			assert.Equal(t, value, got, "key %q", key)
		}
	}
}

func TestUnaliasedRejectsRecursiveAlias(t *testing.T) {
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("loop: &x\n  again: *x\n"), &node))
	budget := maxExportNodes
	_, err := unaliased(&node, &budget)
	require.ErrorContains(t, err, "contains it")
	assert.Greater(t, budget, maxExportNodes-10, "fails before copying the cycle out")
}
