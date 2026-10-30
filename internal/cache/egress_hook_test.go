package cache

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/egress"
)

// Redis speaks its own protocol over raw TCP and reads no proxy variable, so
// an egress policy reaches it only through the hook. The dial is refused on
// purpose: what matters is that the hook was asked, and for the address the
// URL names.
func TestRedisStoreDialsThroughTheEgressHook(t *testing.T) {
	var address string
	hook, err := egress.Install(func(_ context.Context, _, addr string) (net.Conn, error) {
		address = addr
		return nil, errors.New("refused by the test hook")
	})
	require.NoError(t, err)
	defer hook.Uninstall()

	_, err = NewRedisStore(RedisStoreConfig{URL: "redis://cache.example.com:6379/0"})
	require.Error(t, err, "expected the refused dial to fail the connection")
	assert.Equal(t, "cache.example.com:6379", address, "hook must see the configured cache address")
}
