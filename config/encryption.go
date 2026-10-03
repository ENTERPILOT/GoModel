package config

import "context"

// KeyWrapper wraps the data keys that encrypt dashboard-managed secrets at
// rest. GoModel generates one random 256-bit data key per database and stores
// it only in wrapped form; by default the key-encryption key is derived from
// GOMODEL_ENCRYPTION_KEY. An extension sets a KeyWrapper with
// LoadResult.SetKeyWrapper to wrap data keys with a KMS instead.
//
// ID names the wrapper and is stored with every key it wraps; only a wrapper
// with the same id is asked to unwrap that key. It must stay stable across
// restarts. At startup a data key stored under another id is unwrapped with a
// previous wrapper passed to SetKeyWrapper, or with GOMODEL_ENCRYPTION_KEY
// (or GOMODEL_ENCRYPTION_KEY_PREVIOUS) when it was wrapped locally, and then
// re-wrapped with this wrapper.
//
// Implementations must never log or return the data key in an error.
type KeyWrapper interface {
	ID() string
	WrapKey(ctx context.Context, dek []byte) ([]byte, error)
	UnwrapKey(ctx context.Context, wrapped []byte) ([]byte, error)
}

// SetKeyWrapper replaces the key-encryption key derived from
// GOMODEL_ENCRYPTION_KEY with w for this configuration generation. Call it
// from run.Options.SetupConfig, before the application opens storage. A
// reload keeps the wrappers of the previous generation unless
// run.Options.ReloadConfig sets others.
//
// previous lists wrappers w replaces, for example after moving to another
// KMS key: data keys they wrapped are re-wrapped with w at startup, after
// which they can be dropped.
func (r *LoadResult) SetKeyWrapper(w KeyWrapper, previous ...KeyWrapper) {
	if r != nil {
		r.keyWrapper = w
		r.previousKeyWrappers = previous
	}
}

// KeyWrapper returns the wrapper set with SetKeyWrapper, or nil.
func (r *LoadResult) KeyWrapper() KeyWrapper {
	if r == nil {
		return nil
	}
	return r.keyWrapper
}

// PreviousKeyWrappers returns the previous wrappers passed to SetKeyWrapper.
func (r *LoadResult) PreviousKeyWrappers() []KeyWrapper {
	if r == nil {
		return nil
	}
	return r.previousKeyWrappers
}
