package config

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportYAMLRedactsHeaderLineCredentials(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_PROXY_KEY", "held-do-not-leak")
	const prefix = "GOMODEL_GUARDRAILS_RULES_0_CONFIG_"
	first := loadYAML(t, []byte(`
guardrails:
  rules:
    - name: proxy
      type: header_edit
      config:
        upstream_set: |
          X-Team: platform
          X-Proxy-Key: proxy-do-not-leak
          # X-Old-Key: old-do-not-leak
          X-Litellm-Key: Bearer litellm-do-not-leak
          X-Upstream: Basic dXBzdHJlYW06ZG8tbm90LWxlYWs=
          X-Held-Key: ${GOMODEL_TEST_PROXY_KEY}
          X-Vault-Key: ${env:GOMODEL_TEST_PROXY_KEY}
          X-Note: Bearer of good news
        response_set:
          - "X-Served-By: gomodel"
          - "Helicone-Auth:list-do-not-leak"
          - "X_Api_Key: Bearer ${env:GOMODEL_TEST_PROXY_KEY}"
        response_remove: |
          X-Request-Id
          X-Api-Key
    - name: judge
      type: llm_judge
      config:
        instructions: |
          Sign-off: the platform team
          Note: Bearer of bad news
`))

	out, err := first.ExportYAML(ExportDocument{Config: first.Config}, []string{"GOMODEL_TEST_PROXY_KEY=held-do-not-leak"})
	require.NoError(t, err)
	body := string(out)

	for _, secret := range []string{"proxy-do-not-leak", "old-do-not-leak", "litellm-do-not-leak", "dXBzdHJlYW06ZG8tbm90LWxlYWs=", "held-do-not-leak", "list-do-not-leak"} {
		assert.NotContains(t, body, secret)
	}
	rule := []string{"guardrails", "rules", "0", "config"}
	assert.Equal(t, strings.Join([]string{
		"X-Team: platform",
		"X-Proxy-Key: ${env:" + prefix + "UPSTREAM_SET_1}",
		"# X-Old-Key: ${env:" + prefix + "UPSTREAM_SET_2}",
		"X-Litellm-Key: Bearer ${env:" + prefix + "UPSTREAM_SET_3}",
		"X-Upstream: Basic ${env:" + prefix + "UPSTREAM_SET_4}",
		"X-Held-Key: ${env:GOMODEL_TEST_PROXY_KEY}",
		"X-Vault-Key: ${env:GOMODEL_TEST_PROXY_KEY}",
		"X-Note: Bearer of good news",
		"",
	}, "\n"), exportedValue(t, body, append(rule, "upstream_set")...))
	assert.Contains(t, body, "# secret not exported: set "+prefix+"UPSTREAM_SET_1, "+prefix+"UPSTREAM_SET_2, "+prefix+"UPSTREAM_SET_3, "+prefix+"UPSTREAM_SET_4\n")
	assert.Equal(t, "X-Served-By: gomodel", exportedValue(t, body, append(rule, "response_set", "0")...))
	assert.Equal(t, "Helicone-Auth:${env:"+prefix+"RESPONSE_SET_1}", exportedValue(t, body, append(rule, "response_set", "1")...))
	assert.Contains(t, body, "# secret not exported: set "+prefix+"RESPONSE_SET_1\n")
	assert.Equal(t, "X_Api_Key: Bearer ${env:GOMODEL_TEST_PROXY_KEY}", exportedValue(t, body, append(rule, "response_set", "2")...))
	assert.Equal(t, "X-Request-Id\nX-Api-Key\n", exportedValue(t, body, append(rule, "response_remove")...))
	assert.Equal(t, "Sign-off: the platform team\nNote: Bearer of bad news\n", exportedValue(t, body, "guardrails", "rules", "1", "config", "instructions"))

	// The export loads back as the same headers once the variables are set.
	for name, value := range map[string]string{
		"UPSTREAM_SET_1": "proxy-do-not-leak", "UPSTREAM_SET_2": "old-do-not-leak", "UPSTREAM_SET_3": "litellm-do-not-leak",
		"UPSTREAM_SET_4": "dXBzdHJlYW06ZG8tbm90LWxlYWs=", "RESPONSE_SET_1": "list-do-not-leak",
	} {
		t.Setenv(prefix+name, value)
	}
	want, got := first.Config.Guardrails.Rules[0].Config, loadYAML(t, out).Config.Guardrails.Rules[0].Config
	secrets := NewSecrets()
	require.NoError(t, secrets.ResolveFields(context.Background(), "guardrails.rules[0].config", &want))
	require.NoError(t, secrets.ResolveFields(context.Background(), "guardrails.rules[0].config", &got))
	assert.Equal(t, want, got)
}

func TestExportYAMLRedactsCopiesOfHeaderLineCredentials(t *testing.T) {
	const prefix = "GOMODEL_GUARDRAILS_RULES_0_CONFIG_"
	cfg := buildDefaultConfig()
	cfg.Guardrails.Rules = []GuardrailRuleConfig{{Name: "proxy", Type: "header_edit", Config: map[string]any{
		"upstream_set": "X-Proxy-Key: proxy-do-not-leak\nX-Litellm-Key: Bearer litellm-do-not-leak",
		// A copy under a name that is no credential's: all of it is a secret.
		"response_set": "X-Served-By: gomodel\nX-Echo: proxy-do-not-leak",
	}}}
	cfg.Users = []UserConfig{{Path: "/a", Description: "proxy-do-not-leak"}, {Path: "/b", Description: "sends litellm-do-not-leak upstream"}}

	out := exportYAML(t, NewSecrets(), cfg, nil, nil)

	assert.NotContains(t, out, "proxy-do-not-leak")
	assert.NotContains(t, out, "litellm-do-not-leak")
	rule := []string{"guardrails", "rules", "0", "config"}
	assert.Equal(t, "X-Proxy-Key: ${env:"+prefix+"UPSTREAM_SET_0}\nX-Litellm-Key: Bearer ${env:"+prefix+"UPSTREAM_SET_1}", exportedValue(t, out, append(rule, "upstream_set")...))
	assert.Equal(t, "${env:"+prefix+"RESPONSE_SET}", exportedValue(t, out, append(rule, "response_set")...))
	assert.Equal(t, "${env:GOMODEL_USERS_0_DESCRIPTION}", exportedValue(t, out, "users", "0", "description"))
	assert.Equal(t, "${env:GOMODEL_USERS_1_DESCRIPTION}", exportedValue(t, out, "users", "1", "description"))
}

func TestHeaderCredential(t *testing.T) {
	for line, want := range map[string]string{
		"X-Proxy-Key: abc":                     "abc",
		"X-Proxy-Key:abc":                      "abc",
		"  X-Litellm-Key :  Bearer abc  \r":    "Bearer abc",
		"Authorization: Basic dXNlcjpwYXNz":    "Basic dXNlcjpwYXNz",
		"Ocp_Apim_Subscription_Key: abc":       "abc",
		"X-Upstream: Bearer abc":               "Bearer abc",
		"X-Upstream: Bearer\tabc":              "Bearer\tabc",
		"X-Upstream: Basic dXNlcjpwYXNz":       "Basic dXNlcjpwYXNz",
		"X-Upstream: basic dXNlcjpwYXNzMQ==":   "basic dXNlcjpwYXNzMQ==",
		"X-Upstream-Token: token abc":          "token abc",
		"X-Unit: token count":                  "",
		"Mode: Basic auth":                     "",
		"Plan: Basic":                          "",
		"#X-Proxy-Key: abc":                    "abc",
		"# X-Proxy-Key: abc":                   "abc",
		"X-Proxy-Key: a b c":                   "a b c",
		"X-Proxy-Key: prefix-${env:PROXY_KEY}": "prefix-${env:PROXY_KEY}",
		"X-Proxy-Key: ${env:PROXY_KEY}":        "",
		"X-Proxy-Key: Bearer ${env:PROXY_KEY}": "",
		"X-Upstream: Bearer ${file:/run/key}":  "",
		"X-Proxy-Key:":                         "",
		"X-Proxy-Key":                          "",
		"X-Team: platform":                     "",
		"X-Note: Bearer of good news":          "",
		"X-Max-Tokens: 256":                    "",
		"the key: abc":                         "",
		"https://example.com/v1":               "",
		"":                                     "",
	} {
		got := ""
		if start, end := headerCredential(line); start >= 0 {
			got = line[start:end]
		}
		assert.Equal(t, want, got, line)
	}
}
