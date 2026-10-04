package providers

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// credentialEnvFields are resolved after the env overlay, only for providers
// that end up using them (see resolveProviders). Every other provider env
// field is parsed, split, or steers the overlay, so its references are
// resolved before it is read.
var credentialEnvFields = map[providerEnvField]bool{
	providerEnvFieldAPIKey:                   true,
	providerEnvFieldBaseURL:                  true,
	providerEnvFieldProxyURL:                 true,
	providerEnvFieldServiceAccountFile:       true,
	providerEnvFieldServiceAccountJSON:       true,
	providerEnvFieldServiceAccountJSONBase64: true,
}

// resolveSettingEnvVars returns environ with secret references resolved in
// every provider env var that is not a credential, labelled with the variable
// name. OPENAI_MODELS=${file:/run/models} is then split into model IDs, and
// SESSION_STICKY_KEYS or MODEL_FILTER_MAX_PRICE_PER_MTOK parse the resolved
// value, not the reference.
func resolveSettingEnvVars(ctx context.Context, secrets *config.Secrets, environ []string, discovery map[string]DiscoveryConfig) ([]string, error) {
	var resolved []string
	for i, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.Contains(value, "${") {
			continue
		}
		field, isProvider := providerEnvFieldOf(key, discovery)
		if !isProvider || credentialEnvFields[field] {
			continue
		}
		if err := secrets.ResolveFields(ctx, key, &value); err != nil {
			return nil, err
		}
		if resolved == nil {
			resolved = append([]string(nil), environ...)
		}
		resolved[i] = key + "=" + value
	}
	if resolved == nil {
		return environ, nil
	}
	return resolved, nil
}

// providerEnvFieldOf reports which provider field key sets, using the same
// parsing as applyProviderEnvVars.
func providerEnvFieldOf(key string, discovery map[string]DiscoveryConfig) (providerEnvField, bool) {
	for _, providerType := range sortedDiscoveryTypes(discovery) {
		prefix := envPrefix(providerType)
		if !strings.HasPrefix(key, prefix+"_") {
			continue
		}
		if _, field, _, ok := parseProviderEnvKey(prefix, key, discovery[providerType]); ok {
			return field, true
		}
	}
	return 0, false
}

// resolveProviderSelectors resolves type and backend of the config.yaml
// providers, which decide which env vars a provider takes, before the overlay
// reads them. It returns a copy; raw is not modified.
func resolveProviderSelectors(ctx context.Context, secrets *config.Secrets, raw map[string]config.RawProviderConfig) (map[string]config.RawProviderConfig, error) {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		for _, field := range []struct {
			path  string
			value *string
		}{
			{"providers." + name + ".type", &p.Type},
			{"providers." + name + ".backend", &p.Backend},
		} {
			if err := secrets.ResolveFields(ctx, field.path, field.value); err != nil {
				return nil, err
			}
		}
		result[name] = p
	}
	return result, nil
}
