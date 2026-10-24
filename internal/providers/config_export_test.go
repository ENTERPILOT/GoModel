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
	result := &InitResult{
		CredentialResolvedProviders: map[string]config.RawProviderConfig{
			"openai":    {Type: "openai", APIKey: "sk-env", APIKeys: []string{"sk-env", "resolved-env-ref"}},
			"anthropic": {Type: "anthropic", APIKey: "resolved-yaml-ref", APIKeys: []string{"resolved-yaml-ref", "sk-yaml-literal"}},
			"ollama":    {Type: "ollama", BaseURL: "http://localhost:11434"},
		},
		apiKeySources: map[string][][]string{
			"openai":    {{"OPENAI_API_KEY"}, {"OPENAI_API_KEY_2"}},
			"anthropic": {{"providers.anthropic.api_key"}, {"providers.anthropic.api_keys[0]"}},
		},
	}

	exported := result.ExportProviders(secrets)

	assert.Equal(t, "${env:OPENAI_API_KEY}", exported["openai"].APIKey)
	assert.Equal(t, []string{"${vault:env-ref}"}, exported["openai"].APIKeys)
	assert.Equal(t, "${vault:yaml-ref}", exported["anthropic"].APIKey)
	assert.Equal(t, []string{"sk-yaml-literal"}, exported["anthropic"].APIKeys, "a literal is left for the export to redact")
	assert.Empty(t, exported["ollama"].APIKey)
	assert.Nil(t, exported["ollama"].APIKeys)
	assert.Equal(t, []string{"sk-env", "resolved-env-ref"}, result.CredentialResolvedProviders["openai"].APIKeys, "the resolved map is not modified")
}

func TestManagedProviderCredentialExportConfigSplitsKeys(t *testing.T) {
	raw := ManagedProviderCredential{Type: "openai", APIKeys: []string{"${env:A}", "${env:B}"}, Models: []string{"gpt-4o"}}.ExportConfig()

	assert.Equal(t, "${env:A}", raw.APIKey)
	assert.Equal(t, []string{"${env:B}"}, raw.APIKeys)
	assert.Equal(t, []config.RawProviderModel{{ID: "gpt-4o"}}, raw.Models)
}
