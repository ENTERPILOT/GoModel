package providers

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// resolveProviderEnvSecrets returns environ with the secret references in
// provider env vars (OPENAI_API_KEY=${file:/run/secrets/openai}) resolved.
// Other variables are left alone: they are not provider configuration.
//
// A reference that cannot be resolved is an error, never a value that later
// filtering drops or forwards upstream. A legacy ${VAR} is not a reference and
// keeps its historical treatment.
func resolveProviderEnvSecrets(ctx context.Context, secrets *config.Secrets, environ []string, discovery map[string]DiscoveryConfig) ([]string, error) {
	var resolved []string
	for i, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.Contains(value, "${") || !isProviderEnvKey(key, discovery) {
			continue
		}
		// The variable is the field: errors name what the operator set, and
		// key rotation (see keyRotation) finds API keys by it.
		next, err := secrets.ResolveField(ctx, key, value)
		if err != nil {
			return nil, err
		}
		if next == value {
			continue
		}
		if resolved == nil {
			resolved = append([]string(nil), environ...)
		}
		resolved[i] = key + "=" + next
	}
	if resolved == nil {
		return environ, nil
	}
	return resolved, nil
}

// isProviderEnvKey reports whether key configures a provider, using the same
// parsing as applyProviderEnvVars.
func isProviderEnvKey(key string, discovery map[string]DiscoveryConfig) bool {
	_, ok := providerEnvKeyField(key, discovery)
	return ok
}

// isProviderEnvEntry is isProviderEnvKey for a KEY=value environ entry.
func isProviderEnvEntry(entry string, discovery map[string]DiscoveryConfig) bool {
	key, _, _ := strings.Cut(entry, "=")
	return isProviderEnvKey(key, discovery)
}

// isProviderEnvAPIKey reports whether key sets a provider API key:
// <PROVIDER>[_<SUFFIX>]_API_KEY[_<n>].
func isProviderEnvAPIKey(key string, discovery map[string]DiscoveryConfig) bool {
	field, ok := providerEnvKeyField(key, discovery)
	return ok && field == providerEnvFieldAPIKey
}

func providerEnvKeyField(key string, discovery map[string]DiscoveryConfig) (providerEnvField, bool) {
	for providerType, spec := range discovery {
		prefix := envPrefix(providerType)
		if !strings.HasPrefix(key, prefix+"_") {
			continue
		}
		if _, field, _, ok := parseProviderEnvKey(prefix, key, spec); ok {
			return field, true
		}
	}
	return 0, false
}
