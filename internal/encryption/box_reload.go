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
// per minReloadInterval unless force is set. It reports whether a reload
// happened.
func (b *Box) reloadKeys(force bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reload == nil || (!force && time.Since(b.lastReload) < minReloadInterval) {
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
	defer next.mu.RUnlock()
	b.keys, b.active = next.keys, next.active
	slog.Info("reloaded encryption keys", "active_key_id", b.active)
	return true
}
