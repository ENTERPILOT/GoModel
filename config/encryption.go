package config

import "context"

// KeyWrapper wraps the data keys that encrypt dashboard-managed secrets at
// rest. GoModel generates one random 256-bit data key per database and stores
// it only in wrapped form; by default the key-encryption key is derived from
// GOMODEL_ENCRYPTION_KEY. An extension sets a KeyWrapper with
// LoadResult.SetKeyWrapper to wrap data keys with a KMS instead.
//
// ID names the wrapper and is stored with every key it wraps. It must stay
// stable across restarts, and change when the underlying key does: a data
// key wrapped under a different id is unwrapped with GOMODEL_ENCRYPTION_KEY
// (or GOMODEL_ENCRYPTION_KEY_PREVIOUS) and re-wrapped with this wrapper at
// startup.
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
// reload keeps the wrapper of the previous generation unless
// run.Options.ReloadConfig sets another one.
func (r *LoadResult) SetKeyWrapper(w KeyWrapper) {
	if r != nil {
		r.keyWrapper = w
	}
}

// KeyWrapper returns the wrapper set with SetKeyWrapper, or nil.
func (r *LoadResult) KeyWrapper() KeyWrapper {
	if r == nil {
		return nil
	}
	return r.keyWrapper
}
