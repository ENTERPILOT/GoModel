package responsecache

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/egress"
)

// The pgvector store opens its own pool, so it needs the hook wired
// separately from the PostgreSQL storage backend. The dial is refused on
// purpose: what matters is that the hook was asked, and for the host the
// URL names rather than an address pgx resolved on its own.
func TestPGVectorStoreDialsThroughTheEgressHook(t *testing.T) {
	var address string
	hook, err := egress.Install(func(_ context.Context, _, addr string) (net.Conn, error) {
		address = addr
		return nil, errors.New("refused by the test hook")
	})
	require.NoError(t, err)
	defer hook.Uninstall()

	_, err = newPGVectorStore(config.PGVectorConfig{
		URL:       "postgres://user:pw@vectors.example.com:5432/gomodel",
		Dimension: 8,
	})
	require.Error(t, err, "expected the refused dial to fail the store")
	assert.Equal(t, "vectors.example.com:5432", address, "hook must see the configured vector store address")
}
