package storage

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/enterpilot/gomodel/egress"
)

// The database clients speak their own protocols over raw TCP and read no
// proxy variable, so an egress policy reaches them only through the hook.
// These tests fail the dial on purpose: what matters is that the hook was
// asked at all, and with the address the URL names.
func TestPostgreSQLDialsThroughTheEgressHook(t *testing.T) {
	address, restore := recordDials(t)
	if _, err := NewPostgreSQL(context.Background(), PostgreSQLConfig{URL: "postgres://user:pw@db.example.com:5432/gomodel"}); err == nil {
		t.Fatal("expected the refused dial to fail the connection")
	}
	restore()
	if *address != "db.example.com:5432" {
		t.Fatalf("hook saw %q, want the configured database address", *address)
	}
}

func TestMongoDBDialsThroughTheEgressHook(t *testing.T) {
	address, restore := recordDials(t)
	_, err := NewMongoDB(context.Background(), MongoDBConfig{
		URL: "mongodb://db.example.com:27017/gomodel?serverSelectionTimeoutMS=200&connectTimeoutMS=200",
	})
	restore()
	if err == nil {
		t.Fatal("expected the refused dial to fail the connection")
	}
	if *address != "db.example.com:27017" {
		t.Fatalf("hook saw %q, want the configured database address", *address)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
