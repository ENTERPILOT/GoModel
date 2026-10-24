package storage

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/egress"
)

// The database clients speak their own protocols over raw TCP and read no
// proxy variable, so an egress policy reaches them only through the hook.
// These tests fail the dial on purpose: what matters is that the hook was
// asked at all, and with the address the URL names.
func TestPostgreSQLDialsThroughTheEgressHook(t *testing.T) {
	address, restore := recordDials(t)
	_, err := NewPostgreSQL(context.Background(), PostgreSQLConfig{URL: "postgres://user:pw@db.example.com:5432/gomodel"})
	restore()
	require.Error(t, err, "expected the refused dial to fail the connection")
	assert.Equal(t, "db.example.com:5432", *address, "hook must see the configured database address")
}

func TestMongoDBDialsThroughTheEgressHook(t *testing.T) {
	address, restore := recordDials(t)
	_, err := NewMongoDB(context.Background(), MongoDBConfig{
		URL: "mongodb://db.example.com:27017/gomodel?serverSelectionTimeoutMS=200&connectTimeoutMS=200",
	})
	restore()
	require.Error(t, err, "expected the refused dial to fail the connection")
	assert.Equal(t, "db.example.com:27017", *address, "hook must see the configured database address")
}

// recordDials installs a hook that refuses every connection and records the
// last address it was asked for. The returned func removes it again; call it
// before asserting, so a failure never leaves the hook installed for the
// rest of the package's tests.
func recordDials(t *testing.T) (*string, func()) {
	t.Helper()
	var address string
	hook, err := egress.Install(func(_ context.Context, _, addr string) (net.Conn, error) {
		address = addr
		return nil, errors.New("refused by the test hook")
	})
	require.NoError(t, err)
	removed := false
	remove := func() {
		if !removed {
			removed = true
			hook.Uninstall()
		}
	}
	t.Cleanup(remove)
	return &address, remove
}
