package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func TestSQLiteReadPool(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "reads.db")})
	require.NoError(t, err)
	writer, reader := store.DB(), store.ReadDB()
	require.NotSame(t, writer, reader)
	assert.Equal(t, sqliteReaderConns(), reader.Stats().MaxOpenConnections)

	_, err = writer.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO items (id) VALUES (1)`)
	require.NoError(t, err)

	var count int
	require.NoError(t, reader.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count))
	assert.Equal(t, 1, count, "a read after a commit sees it")

	var queryOnly int
	require.NoError(t, reader.QueryRow(`PRAGMA query_only`).Scan(&queryOnly))
	assert.Equal(t, 1, queryOnly)
	_, err = reader.Exec(`INSERT INTO items (id) VALUES (2)`)
	require.Error(t, err, "the read pool rejects writes")

	require.NoError(t, store.Close())
	require.Error(t, reader.Ping(), "Close closes the read pool")
}

func TestSQLiteInMemoryReadsShareWriter(t *testing.T) {
	for _, path := range []string{":memory:", "file::memory:"} {
		t.Run(path, func(t *testing.T) {
			store, err := NewSQLite(SQLiteConfig{Path: path})
			require.NoError(t, err)
			defer store.Close()

			// A second connection would open its own, empty database.
			require.Same(t, store.DB(), store.ReadDB())
			_, err = store.DB().Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY)`)
			require.NoError(t, err)
			var count int
			require.NoError(t, store.ReadDB().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

// TestSQLiteWritesDoNotWaitForReads holds a read open on the read pool while
// the writer commits: no write waits for the read, and the read pool sees
// every row written.
func TestSQLiteWritesDoNotWaitForReads(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "concurrent.db")})
	require.NoError(t, err)
	defer store.Close()
	writer, reader := store.DB(), store.ReadDB()
	_, err = writer.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)

	// An unbounded scan, read one row at a time, holds a read connection and
	// its read transaction open until it is closed.
	read, err := reader.Query(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n) SELECT i FROM n`)
	require.NoError(t, err)
	defer read.Close()
	require.True(t, read.Next())

	const writes = 50
	for i := range writes {
		// Bounded by the busy timeout: a write queued behind the read would
		// wait until the read ends, which here is never.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := writer.ExecContext(ctx, `INSERT INTO items (id) VALUES (?)`, i)
		cancel()
		require.NoError(t, err)
	}

	var count int
	require.NoError(t, reader.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count))
	assert.Equal(t, writes, count)
	assert.True(t, read.Next(), "the read is still open")
}

type closeOnlyStorage struct{}

func (closeOnlyStorage) Close() error { return nil }

func TestResolveSQLReadBackend(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "resolve.db")})
	require.NoError(t, err)
	defer store.Close()
	writer, err := sqlx.NewSQLite(store.DB())
	require.NoError(t, err)

	reads, err := ResolveSQLReadBackend(ctx, store,
		func(db sqlx.DB) (sqlx.DB, error) { return db, nil },
		func(*mongo.Database) (sqlx.DB, error) { return nil, nil },
	)
	require.NoError(t, err)
	_, err = reads.Exec(ctx, `CREATE TABLE items (id INTEGER PRIMARY KEY)`)
	require.Error(t, err, "SQLite read stores get the read-only pool")
	_, err = writer.Exec(ctx, `CREATE TABLE items (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)

	assert.Equal(t, writer, ReadSQL(closeOnlyStorage{}, writer), "other backends read through db")
}
