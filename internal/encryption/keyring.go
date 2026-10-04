package encryption

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// Options selects the key-encryption key.
type Options struct {
	// Key is GOMODEL_ENCRYPTION_KEY. It derives the local KEK.
	Key string
	// PreviousKey is GOMODEL_ENCRYPTION_KEY_PREVIOUS: a local KEK that is
	// only used to unwrap data keys so they can be re-wrapped with the
	// current one.
	PreviousKey string
	// Wrapper is an extension's KMS-backed wrapper. When set it replaces the
	// local KEK; Key and PreviousKey then only unwrap data keys that still
	// need to be moved to it.
	Wrapper Wrapper
	// PreviousWrappers unwrap data keys held by wrappers Wrapper replaces,
	// so they can be re-wrapped with it.
	PreviousWrappers []Wrapper
}

func (o Options) current() kek {
	switch {
	case o.Wrapper != nil:
		return extensionKEK{wrapper: o.Wrapper}
	case o.Key != "":
		return newLocalKEK(o.Key)
	default:
		return nil
	}
}

// fallbacks are KEKs that may unwrap a data key but never wrap one.
func (o Options) fallbacks() []kek {
	var out []kek
	for _, w := range o.PreviousWrappers {
		if w != nil {
			out = append(out, extensionKEK{wrapper: w})
		}
	}
	if o.Wrapper != nil && o.Key != "" {
		out = append(out, newLocalKEK(o.Key))
	}
	if o.PreviousKey != "" && o.PreviousKey != o.Key {
		out = append(out, newLocalKEK(o.PreviousKey))
	}
	return out
}

// Open loads the database's data keys and returns the Box that seals and
// opens secret fields with them.
//
// Without a configured KEK it returns a disabled Box, unless the database
// already holds data keys: their secrets would be unreadable, so that is an
// error. With a KEK and no data key yet, it creates the first one. A data key
// wrapped by an older KEK (GOMODEL_ENCRYPTION_KEY_PREVIOUS, or the local KEK
// when an extension wrapper is now configured) is re-wrapped with the current
// one.
func Open(ctx context.Context, store KeyStore, opts Options) (*Box, error) {
	current := opts.current()
	keys, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	if current == nil {
		if len(keys) > 0 {
			return nil, errors.New("the database holds encrypted secrets but GOMODEL_ENCRYPTION_KEY is not set; " +
				"set it to the key this database was encrypted with")
		}
		return Disabled(), nil
	}
	if len(keys) == 0 {
		if keys, err = createFirstKey(ctx, store, current); err != nil {
			return nil, err
		}
	}

	deks := make(map[string][]byte, len(keys))
	for _, key := range keys {
		dek, err := unwrapKey(ctx, store, key, current, opts.fallbacks())
		if err != nil {
			return nil, err
		}
		deks[key.ID] = dek
	}
	active, err := activeKeyID(keys)
	if err != nil {
		return nil, err
	}
	box, err := newBox(active, deks)
	if err != nil {
		return nil, err
	}
	box.reload = func() (*Box, error) {
		ctx, cancel := context.WithTimeout(context.Background(), reloadTimeout)
		defer cancel()
		return Open(ctx, store, opts)
	}
	box.activeID = func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), reloadTimeout)
		defer cancel()
		keys, err := store.List(ctx)
		if err != nil {
			return "", err
		}
		return activeKeyID(keys)
	}
	return box, nil
}

// reloadTimeout bounds a key-store reload triggered by a read.
const reloadTimeout = 30 * time.Second

// RotateDataKey creates a new data key, wraps it with the current KEK, and
// makes it active. Values sealed with older data keys stay readable; `gomodel
// secrets reencrypt` moves them to the new one.
func RotateDataKey(ctx context.Context, store KeyStore, opts Options) (*Box, error) {
	current := opts.current()
	if current == nil {
		return nil, errors.New("rotating the data key needs GOMODEL_ENCRYPTION_KEY or a key wrapper")
	}
	if _, err := Open(ctx, store, opts); err != nil {
		return nil, err
	}
	keys, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	key, err := newWrappedKey(ctx, current, strconv.Itoa(maxKeyID(keys)+1))
	if err != nil {
		return nil, err
	}
	inserted, err := store.Insert(ctx, key)
	if err != nil {
		return nil, err
	}
	if !inserted {
		return nil, fmt.Errorf("data key %q was created concurrently; retry", key.ID)
	}
	if err := store.Activate(ctx, key.ID); err != nil {
		return nil, err
	}
	slog.Info("created and activated a new data key", "key_id", key.ID, "wrapper", current.id())
	return Open(ctx, store, opts)
}

func createFirstKey(ctx context.Context, store KeyStore, current kek) ([]Key, error) {
	key, err := newWrappedKey(ctx, current, "1")
	if err != nil {
		return nil, err
	}
	key.Active = true
	inserted, err := store.Insert(ctx, key)
	if err != nil {
		return nil, err
	}
	if inserted {
		slog.Info("created the data key for encrypting secrets at rest", "key_id", key.ID, "wrapper", current.id())
	}
	// Re-read either way: another instance may have won the insert.
	return store.List(ctx)
}

func newWrappedKey(ctx context.Context, current kek, id string) (Key, error) {
	dek := make([]byte, dekSize)
	if _, err := rand.Read(dek); err != nil {
		return Key{}, fmt.Errorf("generate data key: %w", err)
	}
	key := Key{ID: id, CreatedAt: time.Now().UTC()}
	if err := current.wrap(ctx, &key, dek); err != nil {
		return Key{}, fmt.Errorf("wrap data key with %q: %w", current.id(), err)
	}
	return key, nil
}

// unwrapKey unwraps one data key with the first KEK that can, and re-wraps it
// with the current KEK when another one succeeded.
func unwrapKey(ctx context.Context, store KeyStore, key Key, current kek, fallbacks []kek) ([]byte, error) {
	// Index 0 is the current KEK. Indexes rather than interface comparison:
	// an extension's wrapper type need not be comparable.
	all := append([]kek{current}, fallbacks...)
	tried := false
	var lastErr error
	for i, k := range all {
		if k.id() != key.WrapperID {
			continue
		}
		tried = true
		dek, err := k.unwrap(ctx, key)
		if err != nil {
			lastErr = err
			continue
		}
		if i != 0 {
			if err := rewrap(ctx, store, key, current, dek); err != nil {
				return nil, err
			}
		}
		return dek, nil
	}
	if !tried {
		return nil, missingWrapperError(key, current)
	}
	if key.WrapperID == LocalWrapperID && errors.Is(lastErr, errWrongKey) {
		return nil, fmt.Errorf("GOMODEL_ENCRYPTION_KEY does not decrypt data key %q; "+
			"if you rotated the key, set GOMODEL_ENCRYPTION_KEY_PREVIOUS to the previous value", key.ID)
	}
	return nil, fmt.Errorf("unwrap data key %q with %q: %w", key.ID, key.WrapperID, lastErr)
}

func missingWrapperError(key Key, current kek) error {
	if key.WrapperID == LocalWrapperID {
		return fmt.Errorf("data key %q is wrapped with GOMODEL_ENCRYPTION_KEY, but only key wrapper %q is configured; "+
			"set GOMODEL_ENCRYPTION_KEY to the old value once so it can be re-wrapped", key.ID, current.id())
	}
	return fmt.Errorf("data key %q is wrapped by key wrapper %q, which is not configured (current: %q); "+
		"pass it as a previous key wrapper so the key can be re-wrapped", key.ID, key.WrapperID, current.id())
}

func rewrap(ctx context.Context, store KeyStore, key Key, current kek, dek []byte) error {
	previous := key.WrapperID
	if err := current.wrap(ctx, &key, dek); err != nil {
		return fmt.Errorf("re-wrap data key %q with %q: %w", key.ID, current.id(), err)
	}
	if err := store.UpdateWrapping(ctx, key); err != nil {
		return err
	}
	slog.Info("re-wrapped data key with the current key-encryption key",
		"key_id", key.ID, "from", previous, "to", current.id(),
		"hint", "GOMODEL_ENCRYPTION_KEY_PREVIOUS can be removed once every instance has restarted")
	return nil
}

// activeKeyID picks the active key, preferring the newest when a concurrent
// rotation briefly left two marked active.
func activeKeyID(keys []Key) (string, error) {
	best, bestN := "", -1
	for _, key := range keys {
		if !key.Active {
			continue
		}
		if n := keyNumber(key.ID); n > bestN {
			best, bestN = key.ID, n
		}
	}
	if best == "" {
		return "", errors.New("encryption_keys has no active data key")
	}
	return best, nil
}

func maxKeyID(keys []Key) int {
	highest := 0
	for _, key := range keys {
		highest = max(highest, keyNumber(key.ID))
	}
	return highest
}

func keyNumber(id string) int {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0
	}
	return n
}
