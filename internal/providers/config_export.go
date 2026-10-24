package providers

import (
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// ExportProviders returns the providers Init resolved, from config.yaml and
// environment variables, for a configuration export (see
// config.Secrets.ExportYAML). Each API key is written as configured: the
// secret reference it held, or ${env:NAME} for a key an environment variable
// supplied as is. A key written literally in config.yaml stays resolved, for
// the export to redact. Other settings stay resolved too; the export restores
// their references by field path.
func (r *InitResult) ExportProviders(secrets *config.Secrets) map[string]config.RawProviderConfig {
	if r == nil {
		return nil
	}
	result := make(map[string]config.RawProviderConfig, len(r.CredentialResolvedProviders))
	for name, p := range r.CredentialResolvedProviders {
		sources := r.apiKeySources[name]
		keys := make([]string, len(p.APIKeys))
		for i, key := range p.APIKeys {
			keys[i] = key
			if i < len(sources) {
				keys[i] = configuredKey(secrets, key, sources[i])
			}
		}
		p.APIKey, p.APIKeys = splitKeys(keys)
		p.Models = append([]config.RawProviderModel(nil), p.Models...)
		result[name] = p
	}
	return result
}

// configuredKey returns key as its first source configured it.
func configuredKey(secrets *config.Secrets, key string, sources []string) string {
	if len(sources) == 0 {
		return key
	}
	if original, ok := secrets.ConfiguredValue(sources[0]); ok {
		return original
	}
	if !strings.HasPrefix(sources[0], "providers.") {
		return "${env:" + sources[0] + "}"
	}
	return key
}

// splitKeys lays out an ordered key set as config.yaml reads it: the first
// key under api_key, the rest under api_keys.
func splitKeys(keys []string) (string, []string) {
	if len(keys) == 0 {
		return "", nil
	}
	if len(keys) == 1 {
		return keys[0], nil
	}
	return keys[0], keys[1:]
}

// ExportConfig returns the credential as a config.yaml provider entry, its
// stored values unresolved, for a configuration export.
func (m ManagedProviderCredential) ExportConfig() config.RawProviderConfig {
	raw := m.toRawProviderConfig()
	raw.APIKey, raw.APIKeys = splitKeys(m.APIKeys)
	return raw
}

// keySourcesByName collects the API key sources of each resolved provider.
func keySourcesByName(providers map[string]ProviderConfig) map[string][][]string {
	sources := make(map[string][][]string, len(providers))
	for name, p := range providers {
		sources[name] = p.APIKeySources
	}
	return sources
}
