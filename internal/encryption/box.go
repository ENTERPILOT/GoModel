// Package encryption encrypts dashboard-managed secrets at rest.
//
// Secret fields (provider API keys, MCP headers, guardrail secrets) are sealed
// with AES-256-GCM under a data key (DEK) before a store writes them. Each
// database holds its own random DEKs in the encryption_keys table, wrapped by
// a key-encryption key (KEK): either one derived from GOMODEL_ENCRYPTION_KEY
// with Argon2id, or an extension's KeyWrapper (a KMS). See ADR-0014.
//
// A sealed value reads enc:v1:<key-id>:<base64(nonce||ciphertext)>. The
// additional authenticated data binds it to one entity and field, so a
// ciphertext copied to another row or field fails to open instead of
// decrypting there.
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// sealedPrefix marks a sealed value and names its format version.
const sealedPrefix = "enc:v1:"

// dekSize is the data key length: AES-256.
const dekSize = 32

// ErrKeyRequired is returned when a sealed value is read without an
// encryption key configured.
var ErrKeyRequired = errors.New("value is encrypted but GOMODEL_ENCRYPTION_KEY is not set")

// errUnknownKey reports a value sealed with a data key this Box does not
// hold. The key id is deliberately left out of the message: it is read from
// the stored value, and errors from this package must not echo stored data.
var errUnknownKey = errors.New("value is encrypted with a data key that is not in encryption_keys")

// minReloadInterval bounds how often a Box re-reads encryption_keys after
// meeting an unknown key id, so a corrupt row cannot turn every read into a
// key-store round trip and an Argon2id derivation.
const minReloadInterval = 10 * time.Second

// Box seals and opens secret field values. A Box without keys (Disabled, or a
// nil *Box) passes plaintext through unchanged, which is the behaviour of a
// deployment without GOMODEL_ENCRYPTION_KEY.
//
// A Box is safe for concurrent use.
type Box struct {
	mu     sync.RWMutex
	active string
	keys   map[string]cipher.AEAD

	// reload re-reads the key store. A running gateway uses it to pick up a
	// data key that `secrets reencrypt --rotate-data-key` created after the
	// gateway started. Nil for a Box that cannot reload.
	reload     func() (*Box, error)
	lastReload time.Time

	plaintextOnce sync.Once
}

// Disabled returns a Box that stores secrets in plaintext. It still logs one
// warning the first time it reads a plaintext secret.
func Disabled() *Box {
	return &Box{}
}

// newBox builds a Box over unwrapped data keys, sealing new values with
// active.
func newBox(active string, deks map[string][]byte) (*Box, error) {
	if _, ok := deks[active]; !ok {
		return nil, fmt.Errorf("active data key %q is not loaded", active)
	}
	keys := make(map[string]cipher.AEAD, len(deks))
	for id, dek := range deks {
		aead, err := newAEAD(dek)
		if err != nil {
			return nil, fmt.Errorf("data key %q: %w", id, err)
		}
		keys[id] = aead
	}
	return &Box{active: active, keys: keys}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != dekSize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), dekSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Enabled reports whether the Box seals new values. It never changes over
// the life of a Box: a reload only adds keys or moves the active one.
func (b *Box) Enabled() bool {
	return b.ActiveKeyID() != ""
}

// ActiveKeyID returns the id of the data key new values are sealed with, or
// "" when encryption is disabled.
func (b *Box) ActiveKeyID() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.active
}

// sealingKey returns the active key and its id.
func (b *Box) sealingKey() (string, cipher.AEAD) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.active, b.keys[b.active]
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
	if b.reloadKeys() {
		b.mu.RLock()
		aead, ok = b.keys[id]
		b.mu.RUnlock()
	}
	return aead, ok
}

// reloadKeys adopts the key store's current keys and active key. It reports
// whether a reload happened.
func (b *Box) reloadKeys() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reload == nil || time.Since(b.lastReload) < minReloadInterval {
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

// AAD builds the additional authenticated data for one secret field.
func AAD(kind, id, field string) []byte {
	return []byte(kind + "\x00" + id + "\x00" + field)
}

// IsSealed reports whether value is a sealed value rather than plaintext.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, sealedPrefix)
}

// IsCurrent reports whether value is sealed with the active data key, so
// re-encryption can skip it.
func (b *Box) IsCurrent(value string) bool {
	keyID, _, ok := splitSealed(value)
	return ok && b.Enabled() && keyID == b.ActiveKeyID()
}

// Seal encrypts plaintext under the active data key. Empty values and every
// value of a disabled Box are returned unchanged.
func (b *Box) Seal(aad []byte, plaintext string) (string, error) {
	if plaintext == "" || !b.Enabled() {
		return plaintext, nil
	}
	active, aead := b.sealingKey()
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), aad)
	return sealedPrefix + active + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a sealed value. Plaintext is returned as-is: rows written
// before encryption was enabled stay readable and are sealed on their next
// write.
func (b *Box) Open(aad []byte, value string) (string, error) {
	if !IsSealed(value) {
		if value != "" {
			b.notePlaintext()
		}
		return value, nil
	}
	if !b.Enabled() {
		return "", ErrKeyRequired
	}
	keyID, payload, ok := splitSealed(value)
	if !ok {
		return "", errors.New("malformed encrypted value")
	}
	aead, ok := b.openingKey(keyID)
	if !ok {
		return "", errUnknownKey
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("malformed encrypted value")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", errors.New("decryption failed: wrong data key, or the value was moved from another row or field")
	}
	return string(plaintext), nil
}

// splitSealed splits enc:v1:<key-id>:<payload>.
func splitSealed(value string) (keyID, payload string, ok bool) {
	rest, found := strings.CutPrefix(value, sealedPrefix)
	if !found {
		return "", "", false
	}
	keyID, payload, found = strings.Cut(rest, ":")
	if !found || keyID == "" || payload == "" {
		return "", "", false
	}
	return keyID, payload, true
}

// notePlaintext logs once per Box that plaintext secrets were read. Startup
// reads every dashboard-managed entity, so this is the startup warning
// without a separate scan.
func (b *Box) notePlaintext() {
	if b == nil {
		return
	}
	b.plaintextOnce.Do(func() {
		if b.Enabled() {
			slog.Warn("some dashboard-managed secrets are still stored in plaintext; they are encrypted on their next save",
				"hint", "run `gomodel secrets reencrypt` to encrypt them all now")
			return
		}
		slog.Warn("dashboard-managed secrets are stored in plaintext in the database",
			"hint", "set GOMODEL_ENCRYPTION_KEY to encrypt them at rest")
	})
}
