package providers

import (
	"context"
	"errors"
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
		next, err := secrets.Resolve(ctx, value)
		if err != nil {
			// Name the variable the operator set, not an empty field.
			if secretErr, isSecretErr := errors.AsType[*config.SecretError](err); isSecretErr {
				secretErr.Field = key
			}
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
	for providerType, spec := range discovery {
		prefix := envPrefix(providerType)
		if !strings.HasPrefix(key, prefix+"_") {
			continue
		}
		if _, _, _, ok := parseProviderEnvKey(prefix, key, spec); ok {
			return true
		}
	}
	return false
}
