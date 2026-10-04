package providers

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/config"
)

// keyRotation keeps the provider map Init built its providers from, after
// the env overlay and secret resolution, so a rotated key is re-applied
// through the very same normalization and filtering rather than a second copy
// of their rules, and the outcome is swapped into the live keyrings.
//
// Because references are resolved after the env overlay, every provider key
// is recorded as providers.<name>.api_key or providers.<name>.api_keys[i],
// whether config.yaml or an environment variable supplied it.
//
// It holds resolved values, as the provider configs already do; they live for
// the generation like every other resolved credential.
type keyRotation struct {
	mu         sync.Mutex
	merged     map[string]config.RawProviderConfig
	discovery  map[string]DiscoveryConfig
	resilience config.ResilienceConfig
	providers  map[string]ProviderConfig
	keyrings   map[string]*Keyring // registered providers only
}

func newKeyRotation(merged map[string]config.RawProviderConfig, discovery map[string]DiscoveryConfig, resilience config.ResilienceConfig, providers map[string]ProviderConfig, keyrings map[string]*Keyring) *keyRotation {
	return &keyRotation{
		merged:     cloneRawProviders(merged),
		discovery:  discovery,
		resilience: resilience,
		providers:  maps.Clone(providers),
		keyrings:   keyrings,
	}
}

// KeyRotation is a planned in-place swap of provider API keys. Build one with
// InitResult.PlanKeyRotation and install it with Apply.
type KeyRotation struct {
	state     *keyRotation
	merged    map[string]config.RawProviderConfig
	providers map[string]ProviderConfig
	swaps     []string
}

// Providers returns the names of the providers whose key set changes, sorted.
// It is empty when the rotated secrets did not change any effective key, for
// example a YAML key shadowed by an environment variable.
func (k *KeyRotation) Providers() []string {
	if k == nil {
		return nil
	}
	return slices.Clone(k.swaps)
}

// Apply swaps the planned key sets into the providers' keyrings. Requests in
// flight keep the key they already hold.
func (k *KeyRotation) Apply() {
	if k == nil || k.state == nil {
		return
	}
	s := k.state
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range k.swaps {
		s.keyrings[name].Replace(k.providers[name].APIKeys...)
	}
	s.merged, s.providers = k.merged, k.providers
}

// PlanKeyRotation works out whether the fields a secret recheck found changed
// can be applied by swapping provider API keys in place. It returns nil when
// they cannot, and the generation has to be rebuilt instead: a changed field is
// not a provider API key, a provider would gain or lose its last key, or the
// change alters anything about a provider besides its keys.
//
// Provider keys are the fields providers.<name>.api_key and
// providers.<name>.api_keys[i], wherever their value came from.
func (r *InitResult) PlanKeyRotation(recheck *config.SecretRecheck) *KeyRotation {
	if r == nil || r.keys == nil {
		return nil
	}
	return r.keys.plan(recheck)
}

func (s *keyRotation) plan(recheck *config.SecretRecheck) *KeyRotation {
	s.mu.Lock()
	defer s.mu.Unlock()

	merged := maps.Clone(s.merged)
	for _, field := range recheck.Fields() {
		value, _ := recheck.Value(field)
		if !setProviderKey(merged, field, value) {
			return nil
		}
	}

	next, _ := finishProviders(merged, s.resilience, s.discovery)
	swaps, ok := s.keySwaps(next)
	if !ok {
		return nil
	}
	return &KeyRotation{state: s, merged: merged, providers: next, swaps: swaps}
}

// keySwaps lists the providers whose keys differ in next, or reports false
// when anything else differs or a changed provider has no keyring to swap.
func (s *keyRotation) keySwaps(next map[string]ProviderConfig) ([]string, bool) {
	if len(next) != len(s.providers) {
		return nil, false
	}
	var swaps []string
	for name, current := range s.providers {
		candidate, ok := next[name]
		if !ok {
			return nil, false
		}
		if !slices.Equal(current.APIKeys, candidate.APIKeys) {
			if s.keyrings[name] == nil || len(candidate.APIKeys) == 0 {
				return nil, false
			}
			swaps = append(swaps, name)
		}
		current.APIKey, current.APIKeys = "", nil
		candidate.APIKey, candidate.APIKeys = "", nil
		if !reflect.DeepEqual(current, candidate) {
			return nil, false
		}
	}
	slices.Sort(swaps)
	return swaps, true
}

// setProviderKey writes value into raw at a providers.<name>.api_key or
// providers.<name>.api_keys[i] path, reporting false for any other path.
func setProviderKey(raw map[string]config.RawProviderConfig, field, value string) bool {
	rest, ok := strings.CutPrefix(field, "providers.")
	if !ok {
		return false
	}
	if name, found := strings.CutSuffix(rest, ".api_key"); found {
		p, exists := raw[name]
		if !exists {
			return false
		}
		p.APIKey = value
		raw[name] = p
		return true
	}
	head, index, found := strings.Cut(rest, ".api_keys[")
	if !found {
		return false
	}
	i, err := strconv.Atoi(strings.TrimSuffix(index, "]"))
	p, exists := raw[head]
	if err != nil || !strings.HasSuffix(index, "]") || !exists || i < 0 || i >= len(p.APIKeys) {
		return false
	}
	p.APIKeys = slices.Clone(p.APIKeys)
	p.APIKeys[i] = value
	raw[head] = p
	return true
}

func cloneRawProviders(raw map[string]config.RawProviderConfig) map[string]config.RawProviderConfig {
	clone := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		p.APIKeys = slices.Clone(p.APIKeys)
		clone[name] = p
	}
	return clone
}
