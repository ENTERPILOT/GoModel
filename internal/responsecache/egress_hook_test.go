package responsecache

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/egress"
)

// The pgvector store opens its own pool, so it needs the hook wired
// separately from the PostgreSQL storage backend. The dial is refused on
// purpose: what matters is that the hook was asked, and for the host the
// URL names rather than an address pgx resolved on its own.
func TestPGVectorStoreDialsThroughTheEgressHook(t *testing.T) {
	var address string
	if err := egress.Install(func(_ context.Context, _, addr string) (net.Conn, error) {
		address = addr
		return nil, errors.New("refused by the test hook")
	}); err != nil {
		t.Fatal(err)
	}
	defer egress.Uninstall()

	if _, err := newPGVectorStore(config.PGVectorConfig{
		URL:       "postgres://user:pw@vectors.example.com:5432/gomodel",
		Dimension: 8,
	}); err == nil {
		t.Fatal("expected the refused dial to fail the store")
	}
	if address != "vectors.example.com:5432" {
		t.Fatalf("hook saw %q, want the configured vector store address", address)
	}
}
