package runtimesettings

import "context"

// Store persists deployment-wide runtime-setting values.
type Store interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key, value string) error
	// SetDefault stores value only when key has no value yet, and returns
	// whatever is stored afterwards: value, or the one that was already
	// there. It is atomic, so instances racing to initialise the same key
	// all end up with the same winner.
	SetDefault(ctx context.Context, key, value string) (string, error)
}
