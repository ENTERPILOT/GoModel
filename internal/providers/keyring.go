package providers

import "sync/atomic"

// Keyring holds the API keys configured for a single provider instance and
// hands them out one at a time, round robin.
//
// A provider is built once and serves every request, so the credential can no
// longer be a string captured at construction: it is resolved per outbound
// request by calling Next from the provider's header hook. One Keyring is
// shared by all of a provider's HTTP clients, so the rotation is even across
// every endpoint that provider serves.
//
// The zero value is not useful; build one with NewKeyring. A nil *Keyring is
// safe to call and behaves as an empty ring, which lets keyless providers
// (Ollama, vLLM) and direct test constructors skip it entirely.
type Keyring struct {
	keys []string
	next atomic.Uint64
}

// NewKeyring returns a Keyring over keys, preserving order while dropping
// empty and duplicate entries. Duplicates are dropped so that a key repeated
// across `OPENAI_API_KEY` and `OPENAI_API_KEY_1` does not take a double share
// of the rotation. It returns nil when no usable key remains, so callers can
// treat "no credentials" and "no keyring" identically.
func NewKeyring(keys ...string) *Keyring {
	unique := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	if len(unique) == 0 {
		return nil
	}
	return &Keyring{keys: unique}
}

// Next returns the key to authenticate the next outbound request, advancing
// the rotation. It is safe for concurrent use. Next returns "" for an empty
// ring, matching the unconfigured-credential behaviour providers already
// handle (see AuthHeaderConfig.OptionalAPIKey).
//
// Rotation advances per outbound HTTP request, which includes retries: a
// request retried after a 429 is re-sent under the next key rather than
// hammering the one that was just throttled.
func (k *Keyring) Next() string {
	if k == nil || len(k.keys) == 0 {
		return ""
	}
	if len(k.keys) == 1 {
		return k.keys[0]
	}
	i := k.next.Add(1) - 1
	return k.keys[i%uint64(len(k.keys))]
}

// Primary returns the first configured key without advancing the rotation.
// It is the key to use where a stable identity matters more than spreading
// load, and where an empty ring must stay empty.
func (k *Keyring) Primary() string {
	if k == nil || len(k.keys) == 0 {
		return ""
	}
	return k.keys[0]
}

// Len reports how many distinct keys back the rotation.
func (k *Keyring) Len() int {
	if k == nil {
		return 0
	}
	return len(k.keys)
}

// Rotates reports whether more than one key is configured, and therefore
// whether successive requests will present different credentials. Callers use
// it to warn about the prompt-caching cost of rotation.
func (k *Keyring) Rotates() bool {
	return k.Len() > 1
}
