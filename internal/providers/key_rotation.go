package providers

import (
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/enterpilot/gomodel/config"
)

// keyRotation keeps the state Init built its providers from (see
// providerSources), so a rotated key is re-applied through the very same
// de-duplication and filtering rather than a second copy of their rules, and
// the outcome is swapped into the live keyrings.
//
// Every API key is recorded under the source the operator wrote: its
// config.yaml path ("providers.openai.api_keys[1]") or the environment
// variable that set it ("OPENAI_API_KEY_2"). A changed field is a provider
// key exactly when it is the source a key was resolved under.
//
// It holds resolved values, as the provider configs already do; they live for
// the generation like every other resolved credential.
type keyRotation struct {
	mu         sync.Mutex
	sources    providerSources
	discovery  map[string]DiscoveryConfig
	resilience config.ResilienceConfig
	providers  map[string]ProviderConfig
	keyrings   map[string]*Keyring // registered providers only
}

func newKeyRotation(sources providerSources, discovery map[string]DiscoveryConfig, resilience config.ResilienceConfig, providers map[string]ProviderConfig, keyrings map[string]*Keyring) *keyRotation {
	return &keyRotation{
		sources:    providerSources{providers: maps.Clone(sources.providers), keys: cloneKeySources(sources.keys)},
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
	keys      map[string][]sourcedKey
	providers map[string]ProviderConfig
	swaps     []string
}

// Providers returns the names of the providers whose key set changes, sorted.
// It is empty when the rotated secrets did not change any effective key, for
// example a key that now equals another one of the provider's keys.
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
	s.sources.keys, s.providers = k.keys, k.providers
}

// PlanKeyRotation works out whether the fields a secret recheck found changed
// can be applied by swapping provider API keys in place. It returns nil when
// they cannot, and the generation has to be rebuilt instead: a changed field is
// not a provider API key, a provider would gain or lose its last key, or the
// change alters anything about a provider besides its keys.
func (r *InitResult) PlanKeyRotation(recheck *config.SecretRecheck) *KeyRotation {
	if r == nil || r.keys == nil {
		return nil
	}
	return r.keys.plan(recheck)
}

func (s *keyRotation) plan(recheck *config.SecretRecheck) *KeyRotation {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := cloneKeySources(s.sources.keys)
	for _, field := range recheck.Fields() {
		value, _ := recheck.Value(field)
		if !setKeyBySource(keys, field, value) {
			return nil
		}
	}

	next, _ := finishProviders(providerSources{providers: s.sources.providers, keys: keys}, s.resilience, s.discovery)
	swaps, ok := s.keySwaps(next)
	if !ok {
		return nil
	}
	return &KeyRotation{state: s, keys: keys, providers: next, swaps: swaps}
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
		current.APIKey, current.APIKeys, current.APIKeySources = "", nil, nil
		candidate.APIKey, candidate.APIKeys, candidate.APIKeySources = "", nil, nil
		if !reflect.DeepEqual(current, candidate) {
			return nil, false
		}
	}
	slices.Sort(swaps)
	return swaps, true
}

// setKeyBySource sets the key resolved under source field, reporting false
// when no key was.
func setKeyBySource(keys map[string][]sourcedKey, field, value string) bool {
	for _, sourced := range keys {
		for i := range sourced {
			if sourced[i].Sources[0] == field {
				sourced[i].Value = value
				return true
			}
		}
	}
	return false
}

// cloneKeySources copies the key slices, so patching one entry leaves the
// state it was cloned from intact. Sources are never modified and are shared.
func cloneKeySources(keys map[string][]sourcedKey) map[string][]sourcedKey {
	clone := make(map[string][]sourcedKey, len(keys))
	for name, sourced := range keys {
		clone[name] = slices.Clone(sourced)
	}
	return clone
}
