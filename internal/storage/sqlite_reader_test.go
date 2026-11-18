package storage

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

	_, err = reader.Exec(`INSERT INTO items (id) VALUES (2)`)
	require.Error(t, err, "the read pool rejects writes")

	require.NoError(t, store.Close())
	require.Error(t, reader.Ping(), "Close closes the read pool")
}

func TestSQLiteReadPoolPragmas(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "pragmas.db")})
	require.NoError(t, err)
	defer store.Close()

	tests := []struct {
		pragma string
		want   string
	}{
		{pragma: "query_only", want: "1"},
		{pragma: "journal_mode", want: "wal"},
		{pragma: "busy_timeout", want: "5000"},
		{pragma: "cache_size", want: "-8192"},
		{pragma: "mmap_size", want: "268435456"},
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			require.NoError(t, store.ReadDB().QueryRow("PRAGMA "+tt.pragma).Scan(&got))
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSQLiteMaintenanceRefreshesReadPoolStatistics checks that a read
// connection opened before the writer analyzed the tables plans with the new
// statistics afterwards: without them the planner prefers the equality index
// over the far more selective range.
func TestSQLiteMaintenanceRefreshesReadPoolStatistics(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "stats.db")})
	require.NoError(t, err)
	defer store.Close()
	writer, reader := store.DB(), store.ReadDB()

	_, err = writer.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, model TEXT, ts INTEGER)`)
	require.NoError(t, err)
	_, err = writer.Exec(`CREATE INDEX idx_items_model ON items(model)`)
	require.NoError(t, err)
	_, err = writer.Exec(`CREATE INDEX idx_items_ts ON items(ts)`)
	require.NoError(t, err)
	_, err = writer.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 20000)
		INSERT INTO items (model, ts) SELECT 'model-' || (i % 2), i FROM n`)
	require.NoError(t, err)

	queryPlan := func() string {
		var id, parent, unused int
		var detail string
		require.NoError(t, reader.QueryRow(`EXPLAIN QUERY PLAN
			SELECT COUNT(*) FROM items WHERE model = 'model-1' AND ts BETWEEN 100 AND 200`).
			Scan(&id, &parent, &unused, &detail))
		return detail
	}
	require.Contains(t, queryPlan(), "idx_items_model", "the read connection has loaded the schema without statistics")

	StartMaintenance(store)

	assert.Contains(t, queryPlan(), "idx_items_ts")
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
			// Maintenance reopens read connections, which must not reach the
			// connection that holds the database.
			StartMaintenance(store)
			var count int
			require.NoError(t, store.ReadDB().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

// TestSQLiteReadPoolEscapesPath opens a database whose path holds the
// characters a file: URI treats specially. The read pool must open the same
// file as the writer, not one cut short at '?' or '#' or with '%' decoded.
func TestSQLiteReadPoolEscapesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not allowed in Windows file names")
	}
	dir := filepath.Join(t.TempDir(), "odd?v=1#frag%41")
	require.NoError(t, os.Mkdir(dir, 0o755))
	path := filepath.Join(dir, "reads.db")

	// The writer opens the same file through a hand-escaped URI.
	writerDSN := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=journal_mode(WAL)"}).String()
	writer, err := sql.Open("sqlite", writerDSN)
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	_, err = writer.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO items (id) VALUES (7)`)
	require.NoError(t, err)

	reader, err := openSQLiteReader(writer)
	require.NoError(t, err)
	defer reader.Close()
	require.NotSame(t, writer, reader)

	var id int
	require.NoError(t, reader.QueryRow(`SELECT id FROM items`).Scan(&id))
	assert.Equal(t, 7, id)
	var file string
	require.NoError(t, reader.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file))
	assert.Equal(t, filepath.Base(path), filepath.Base(file))
	assert.Contains(t, file, "odd?v=1#frag%41")
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
	const seeded = 3
	for i := range seeded {
		_, err = writer.Exec(`INSERT INTO items (id) VALUES (?)`, -1-i)
		require.NoError(t, err)
	}

	// A scan of a table in main, read one row at a time, holds a read
	// connection and its read transaction (a read lock on main) open until it
	// is closed.
	read, err := reader.Query(`SELECT id FROM items ORDER BY id`)
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
	assert.Equal(t, seeded+writes, count)
	assert.True(t, read.Next(), "the read is still open")
}

type closeOnlyStorage struct{}

func (closeOnlyStorage) Close() error { return nil }

type mongoOnlyStorage struct{ closeOnlyStorage }

func (mongoOnlyStorage) Database() *mongo.Database { return nil }

func TestResolveSQLReadBackendWithoutSQLStore(t *testing.T) {
	ctx := context.Background()
	got, err := ResolveSQLReadBackend[string](ctx, mongoOnlyStorage{}, nil,
		func(*mongo.Database) (string, error) { return "mongo", nil },
	)
	require.NoError(t, err, "MongoDB does not need the SQL constructor")
	assert.Equal(t, "mongo", got)

	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "nil.db")})
	require.NoError(t, err)
	defer store.Close()
	_, err = ResolveSQLReadBackend[string](ctx, store, nil, nil)
	require.Error(t, err, "SQLite needs the SQL constructor")
}

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
