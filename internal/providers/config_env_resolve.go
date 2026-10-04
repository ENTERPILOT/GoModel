package providers

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// credentialEnvFields are resolved after the env overlay, only for providers
// that end up using them (see resolveProviders). Every other provider env
// field is parsed, split, or steers the overlay, so applyProviderEnvVars
// resolves it before reading it.
var credentialEnvFields = map[providerEnvField]bool{
	providerEnvFieldAPIKey:                   true,
	providerEnvFieldBaseURL:                  true,
	providerEnvFieldProxyURL:                 true,
	providerEnvFieldServiceAccountFile:       true,
	providerEnvFieldServiceAccountJSON:       true,
	providerEnvFieldServiceAccountJSONBase64: true,
}

// resolveSetting resolves the secret references in a provider setting that is
// read before the final pass over the candidates (secrets.ResolveFields in
// resolveProviders). Its result is escaped, every "${" written as "$${", so
// that pass turns it back into the resolved text instead of scanning it a
// second time: a model ID written as $${env:X} stays the literal ${env:X}.
func resolveSetting(ctx context.Context, secrets *config.Secrets, label, value string) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	if err := secrets.ResolveFields(ctx, label, &value); err != nil {
		return "", err
	}
	return strings.ReplaceAll(value, "${", "$${"), nil
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
			resolved, err := resolveSetting(ctx, secrets, field.path, *field.value)
			if err != nil {
				return nil, err
			}
			*field.value = resolved
		}
		result[name] = p
	}
	return result, nil
}

// resolveFilterSettings resolves, in place, the merged provider settings
// whose value, not just presence, the credential filter decides on: a Vertex
// provider's auth_type, which picks whether a service account is required.
// It is looked up only for a Vertex provider with an endpoint, the filter's
// first requirement. An auth_type set by an env var is already resolved, so a
// reference left here was written in config.yaml.
func resolveFilterSettings(ctx context.Context, secrets *config.Secrets, merged map[string]config.RawProviderConfig) error {
	for name, p := range merged {
		if !isVertexProviderConfig(p) || !vertexHasEndpoint(p, providerValueSet) || !config.HasSecretReference(p.AuthType) {
			continue
		}
		resolved, err := resolveSetting(ctx, secrets, "providers."+name+".auth_type", p.AuthType)
		if err != nil {
			return err
		}
		p.AuthType = resolved
		merged[name] = p
	}
	return nil
}
