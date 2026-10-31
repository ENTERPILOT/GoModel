package providers

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// ExportedProviders is the provider section of a configuration export (see
// config.LoadResult.ExportYAML).
type ExportedProviders struct {
	// Providers are the providers config.yaml and environment variables
	// declare whose credentials resolve, as config.yaml entries, keyed by name.
	Providers map[string]config.RawProviderConfig
	// References are the paths of the API keys written as secret references:
	// as configured, or ${env:NAME} for a key a variable supplied as is.
	References []string
	// Declared names every provider config.yaml or environment variables
	// declare. A stored credential of one of these names is shadowed.
	Declared []string
}

// ExportProviders resolves the providers config.yaml and environment
// variables declare, as Init does, without building them, for a
// configuration export. Settings other than API keys stay resolved: the
// export restores their references by field path.
func ExportProviders(ctx context.Context, result *config.LoadResult, factory *ProviderFactory) (ExportedProviders, error) {
	if result == nil || factory == nil {
		return ExportedProviders{}, fmt.Errorf("load result and factory are required")
	}
	discovery := factory.discoveryConfigsSnapshot()
	sources, err := mergeProviderSources(ctx, result.Secrets, result.RawProviders, discovery)
	if err != nil {
		return ExportedProviders{}, err
	}
	providerMap, resolved := finishProviders(sources, result.Config.Resilience, discovery)
	exported := exportProviders(result.Secrets, providerMap, resolved)
	exported.Declared = slices.Sorted(maps.Keys(result.RawProviders))
	for name := range providerMap {
		if !slices.Contains(exported.Declared, name) {
			exported.Declared = append(exported.Declared, name)
		}
	}
	slices.Sort(exported.Declared)
	return exported, nil
}

// exportProviders writes each API key of resolved as its first source
// configured it. A key written literally in config.yaml stays resolved, for
// the export to redact.
func exportProviders(secrets *config.Secrets, providerMap map[string]ProviderConfig, resolved map[string]config.RawProviderConfig) ExportedProviders {
	exported := ExportedProviders{Providers: make(map[string]config.RawProviderConfig, len(resolved))}
	for name, p := range resolved {
		sources := providerMap[name].APIKeySources
		keys := slices.Clone(p.APIKeys)
		for i := range keys {
			if i >= len(sources) {
				continue
			}
			if configured, ok := configuredKey(secrets, sources[i]); ok {
				keys[i] = configured
				exported.References = append(exported.References, apiKeyPath(name, i))
			}
		}
		p.APIKey, p.APIKeys = splitKeys(keys)
		p.Models = slices.Clone(p.Models)
		exported.Providers[name] = p
	}
	slices.Sort(exported.References)
	return exported
}

// configuredKey returns a key as its first source configured it: the secret
// reference it held, or ${env:NAME} for a variable that supplied it as is.
// A key config.yaml holds literally is not returned.
func configuredKey(secrets *config.Secrets, sources []string) (string, bool) {
	if len(sources) == 0 {
		return "", false
	}
	if original, ok := secrets.ConfiguredValue(sources[0]); ok {
		return original, true
	}
	if !strings.HasPrefix(sources[0], "providers.") {
		return "${env:" + sources[0] + "}", true
	}
	return "", false
}

// apiKeyPath returns the path of the i-th key of a provider as splitKeys
// lays the keys out.
func apiKeyPath(name string, i int) string {
	if i == 0 {
		return "providers." + name + ".api_key"
	}
	return "providers." + name + ".api_keys[" + strconv.Itoa(i-1) + "]"
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
// stored values unresolved, for a configuration export, with the fields it
// holds that the gateway resolves secret references in, by their path in
// the export.
func (m ManagedProviderCredential) ExportConfig() (config.RawProviderConfig, []string) {
	raw := m.toRawProviderConfig()
	raw.APIKey, raw.APIKeys = splitKeys(m.APIKeys)
	var references []string
	for i := range m.APIKeys {
		references = append(references, apiKeyPath(m.Name, i))
	}
	prefix := "providers." + m.Name + "."
	for field, value := range map[string]string{
		"service_account_json":        m.ServiceAccountJSON,
		"service_account_json_base64": m.ServiceAccountJSONBase64,
		"proxy_url":                   m.ProxyURL,
	} {
		if value != "" {
			references = append(references, prefix+field)
		}
	}
	slices.Sort(references)
	return raw, references
}
