//go:build duckdb

package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Stores resolved separately from one DuckDB backend still queue their
// writes: a read-then-write transaction in one must not interleave with the
// other's, which DuckDB would answer with a conflict error or a lost update.
func TestDuckDBStoresSerializeWrites(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDB(DuckDBConfig{Path: filepath.Join(t.TempDir(), "test.duckdb")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	resolve := func() sqlx.DB {
		db, err := ResolveSQLBackend(ctx, store, func(db sqlx.DB) (sqlx.DB, error) { return db, nil }, nil)
		require.NoError(t, err)
		return db
	}
	stores := []sqlx.DB{resolve(), resolve()}
	require.NoError(t, stores[0].Schema(ctx, `CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`))
	_, err = stores[0].Exec(ctx, `INSERT INTO counter (id, n) VALUES (1, 0)`)
	require.NoError(t, err)

	const increments = 100
	var wg sync.WaitGroup
	for _, db := range stores {
		wg.Go(func() {
			for range increments {
				assert.NoError(t, db.InTx(ctx, func(q sqlx.Querier) error {
					var n int
					if err := q.QueryRow(ctx, `SELECT n FROM counter WHERE id = 1`).Scan(&n); err != nil {
						return err
					}
					_, err := q.Exec(ctx, `UPDATE counter SET n = ? WHERE id = 1`, n+1)
					return err
				}))
			}
		})
	}
	wg.Wait()

	var n int
	require.NoError(t, stores[0].QueryRow(ctx, `SELECT n FROM counter WHERE id = 1`).Scan(&n))
	assert.Equal(t, len(stores)*increments, n)
}
