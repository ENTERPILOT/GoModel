package runtimesettings

import "context"

// Store persists deployment-wide runtime-setting values.
type Store interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key, value string) error
}
