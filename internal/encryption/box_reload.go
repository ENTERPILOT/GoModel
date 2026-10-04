package encryption

import (
	"crypto/cipher"
	"fmt"
	"log/slog"
	"time"
)

// minReloadInterval bounds how often a read of an unknown key id makes a Box
// re-read encryption_keys, so a corrupt row cannot turn every read into a
// key-store round trip and an Argon2id derivation. Seals are not limited:
// they reload only when the active key really changed.
const minReloadInterval = 10 * time.Second

// sealingKey returns the active key and its id as this Box knows them.
func (b *Box) sealingKey() (string, cipher.AEAD) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.active, b.keys[b.active]
}

// currentSealingKey returns the key the database says is active, reloading
// the key store when it differs from the one this Box holds. It fails rather
// than fall back to a key that may have been replaced.
func (b *Box) currentSealingKey() (string, cipher.AEAD, error) {
	if b.activeID == nil {
		active, aead := b.sealingKey()
		return active, aead, nil
	}
	id, err := b.activeID()
	if err != nil {
		return "", nil, fmt.Errorf("check the active data key: %w", err)
	}
	active, aead := b.sealingKey()
	if id == active {
		return active, aead, nil
	}
	b.reloadKeys(true)
	active, aead = b.sealingKey()
	if id != active {
		return "", nil, fmt.Errorf("data key %q is active but could not be loaded", id)
	}
	return active, aead, nil
}

// openingKey returns the data key with id, re-reading the key store once if
// this Box does not hold it yet.
func (b *Box) openingKey(id string) (cipher.AEAD, bool) {
	b.mu.RLock()
	aead, ok := b.keys[id]
	b.mu.RUnlock()
	if ok {
		return aead, true
	}
	if b.reloadKeys(false) {
		b.mu.RLock()
		aead, ok = b.keys[id]
		b.mu.RUnlock()
	}
	return aead, ok
}

// reloadKeys adopts the key store's current keys and active key, at most once
// per minReloadInterval unless force is set. It reports whether the keys were
// reloaded, by this call or by one that finished while it waited.
//
// The key store is read without holding mu: a reload lists keys and may run
// Argon2id or call a KMS, and reads and seals of keys the Box already holds
// must not wait for that. reloadMu only serializes reloads.
func (b *Box) reloadKeys(force bool) bool {
	if b.reload == nil {
		return false
	}
	requested := time.Now()
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()
	if b.lastReload.After(requested) {
		return true
	}
	if !force && time.Since(b.lastReload) < minReloadInterval {
		return false
	}
	b.lastReload = time.Now()
	next, err := b.reload()
	if err != nil {
		slog.Warn("could not reload encryption keys", "error", err)
		return false
	}
	if !next.Enabled() {
		return false
	}
	next.mu.RLock()
	keys, active := next.keys, next.active
	next.mu.RUnlock()
	b.mu.Lock()
	b.keys, b.active = keys, active
	b.mu.Unlock()
	slog.Info("reloaded encryption keys", "active_key_id", active)
	return true
}
