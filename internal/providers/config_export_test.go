package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

func TestExportProvidersWritesKeysAsConfigured(t *testing.T) {
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		return "resolved-" + reference, nil
	})))
	// Record two keys that held references, as resolveKeySources does.
	for source, value := range map[string]string{"OPENAI_API_KEY_2": "${vault:env-ref}", "providers.anthropic.api_key": "${vault:yaml-ref}"} {
		require.NoError(t, secrets.ResolveFields(context.Background(), source, &value))
	}
	resolved := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: "sk-env", APIKeys: []string{"sk-env", "resolved-env-ref"}},
		"anthropic": {Type: "anthropic", APIKey: "resolved-yaml-ref", APIKeys: []string{"resolved-yaml-ref", "sk-yaml-literal"}},
		"ollama":    {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	providerMap := map[string]ProviderConfig{
		"openai":    {APIKeySources: [][]string{{"OPENAI_API_KEY"}, {"OPENAI_API_KEY_2"}}},
		"anthropic": {APIKeySources: [][]string{{"providers.anthropic.api_key"}, {"providers.anthropic.api_keys[0]"}}},
		"ollama":    {},
	}

	exported := exportProviders(secrets, providerMap, resolved)

	assert.Equal(t, "${env:OPENAI_API_KEY}", exported.Providers["openai"].APIKey)
	assert.Equal(t, []string{"${vault:env-ref}"}, exported.Providers["openai"].APIKeys)
	assert.Equal(t, "${vault:yaml-ref}", exported.Providers["anthropic"].APIKey)
	assert.Equal(t, []string{"sk-yaml-literal"}, exported.Providers["anthropic"].APIKeys, "a literal is left for the export to redact")
	assert.Empty(t, exported.Providers["ollama"].APIKey)
	assert.Nil(t, exported.Providers["ollama"].APIKeys)
	assert.Equal(t, []string{
		"providers.anthropic.api_key",
		"providers.openai.api_key",
		"providers.openai.api_keys[0]",
	}, exported.References)
	assert.Equal(t, []string{"sk-env", "resolved-env-ref"}, resolved["openai"].APIKeys, "the resolved map is not modified")
}

func TestManagedProviderCredentialExportConfig(t *testing.T) {
	raw, references := ManagedProviderCredential{
		Name: "groq", Type: "groq", APIKeys: []string{"${env:A}", "gsk-literal"},
		ProxyURL: "http://proxy:8080", Models: []string{"llama"},
	}.ExportConfig()

	assert.Equal(t, "${env:A}", raw.APIKey)
	assert.Equal(t, []string{"gsk-literal"}, raw.APIKeys)
	assert.Equal(t, []config.RawProviderModel{{ID: "llama"}}, raw.Models)
	assert.Equal(t, []string{"providers.groq.api_key", "providers.groq.api_keys[0]", "providers.groq.proxy_url"}, references)
}
