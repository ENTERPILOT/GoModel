package cache

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/enterpilot/gomodel/egress"
)

// Redis speaks its own protocol over raw TCP and reads no proxy variable, so
// an egress policy reaches it only through the hook. The dial is refused on
// purpose: what matters is that the hook was asked, and for the address the
// URL names.
func TestRedisStoreDialsThroughTheEgressHook(t *testing.T) {
	var address string
	if err := egress.Install(func(_ context.Context, _, addr string) (net.Conn, error) {
		address = addr
		return nil, errors.New("refused by the test hook")
	}); err != nil {
		t.Fatal(err)
	}
	defer egress.Uninstall()

	if _, err := NewRedisStore(RedisStoreConfig{URL: "redis://cache.example.com:6379/0"}); err == nil {
		t.Fatal("expected the refused dial to fail the connection")
	}
	if address != "cache.example.com:6379" {
		t.Fatalf("hook saw %q, want the configured cache address", address)
	}
}
