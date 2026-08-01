package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"sync/atomic"

	"github.com/enterpilot/gomodel/internal/core"
)

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
	keys          []string
	next          atomic.Uint64
	sessionSticky bool
}

// NewKeyring returns a Keyring over keys, preserving order while dropping
// empty and duplicate entries. Duplicates are dropped so that a key repeated
// across `OPENAI_API_KEY` and `OPENAI_API_KEY_1` does not take a double share
// of the rotation. It returns nil when no usable key remains, so callers can
// treat "no credentials" and "no keyring" identically.
func NewKeyring(keys ...string) *Keyring {
	return NewKeyringWithSessionStickiness(true, keys...)
}

// NewKeyringWithSessionStickiness builds a keyring whose identified sessions
// deterministically select one credential. Sessionless traffic always keeps
// the historical round-robin behavior.
func NewKeyringWithSessionStickiness(sessionSticky bool, keys ...string) *Keyring {
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
	return &Keyring{keys: unique, sessionSticky: sessionSticky}
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

// NextForContext returns the key pinned to the request's GoModel session. When
// no session is present, or session stickiness was disabled for this provider,
// it advances the ordinary round-robin sequence.
func (k *Keyring) NextForContext(ctx context.Context) string {
	return k.NextForSession(core.SessionIDFromContext(ctx))
}

// StableForContext returns the credential that every request with this context
// will use without advancing round-robin state. The boolean is false when the
// context has no stable credential affinity, so callers must not persist
// credential-scoped provider resources for later reuse.
func (k *Keyring) StableForContext(ctx context.Context) (string, bool) {
	if k == nil || len(k.keys) == 0 {
		return "", false
	}
	if len(k.keys) == 1 {
		return k.keys[0], true
	}
	sessionID := core.SessionIDFromContext(ctx)
	if !k.sessionSticky || sessionID == "" {
		return "", false
	}
	return k.stickyKey(sessionID), true
}

// NextForSession deterministically maps one non-empty session to one key using
// rendezvous hashing. Adding or removing a key therefore remaps only sessions
// assigned to the changed key, rather than invalidating every warm cache. The
// digest and credential material remain in-process and are never exposed.
func (k *Keyring) NextForSession(sessionID string) string {
	if k == nil || len(k.keys) == 0 {
		return ""
	}
	if len(k.keys) == 1 {
		return k.keys[0]
	}
	if !k.sessionSticky || sessionID == "" {
		return k.Next()
	}
	return k.stickyKey(sessionID)
}

func (k *Keyring) stickyKey(sessionID string) string {
	selected := k.keys[0]
	best := rendezvousKeyScore(sessionID, selected)
	for _, key := range k.keys[1:] {
		score := rendezvousKeyScore(sessionID, key)
		if bytes.Compare(score[:], best[:]) > 0 {
			selected = key
			best = score
		}
	}
	return selected
}

func rendezvousKeyScore(sessionID, key string) [sha256.Size]byte {
	// The credential is the HMAC key, not password material being stored or
	// verified. HMAC also makes that security boundary explicit to static
	// analysis while retaining a deterministic, uniformly distributed score.
	hash := hmac.New(sha256.New, []byte(key))
	_, _ = hash.Write([]byte(sessionID))
	var score [sha256.Size]byte
	copy(score[:], hash.Sum(nil))
	return score
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

// Rotates reports whether more than one key is configured. Identified sessions
// may still remain pinned to one of those keys when session stickiness is on.
func (k *Keyring) Rotates() bool {
	return k.Len() > 1
}
