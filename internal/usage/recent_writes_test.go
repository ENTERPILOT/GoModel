package usage

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// Usage ids are UUID columns on PostgreSQL.
const (
	usageIDOld       = "00000000-0000-4000-8000-000000000001"
	usageIDMid       = "00000000-0000-4000-8000-000000000002"
	usageIDNew       = "00000000-0000-4000-8000-000000000003"
	usageIDUnstamped = "00000000-0000-4000-8000-000000000004"
	usageIDMissing   = "00000000-0000-4000-8000-000000000005"
)

func stampedUsageEntry(id, writer string, writtenAt int64) *UsageEntry {
	return &UsageEntry{
		ID:           id,
		RequestID:    "req-" + id,
		ProviderID:   "provider-" + id,
		Timestamp:    time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Model:        "gpt-5",
		Provider:     "openai",
		Endpoint:     "/v1/chat/completions",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		Stamp:        stampOf(writer, writtenAt),
	}
}

// stampOf is the stamp a logger would put on the entry, or none for an
// empty writer, as on a row an older gateway wrote.
func stampOf(writer string, writtenAt int64) *storage.WriteStamp {
	if writer == "" {
		return nil
	}
	return &storage.WriteStamp{WriterID: writer, WrittenAt: writtenAt}
}

func usageWriteIDs(writes []storage.RecentWrite) []string {
	ids := make([]string, len(writes))
	for i, write := range writes {
		ids[i] = write.ID
	}
	return ids
}

// runRecentWritesSuite runs body against each backend available here; the
// PostgreSQL and MongoDB legs skip without their test databases.
func runRecentWritesSuite(t *testing.T, body func(t *testing.T, store UsageStore, reader RecentWriteReader)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		body(t, store, reader)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, reader := newMongoUsageStore(t, db, 0)
		body(t, store, reader)
	})
}

func TestUsageRecentWrites(t *testing.T) {
	runRecentWritesSuite(t, func(t *testing.T, store UsageStore, reader RecentWriteReader) {
		ctx := context.Background()
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			stampedUsageEntry(usageIDOld, "writer-a", 100),
			stampedUsageEntry(usageIDMid, "writer-b", 200),
			stampedUsageEntry(usageIDNew, "writer-a", 300),
			// Written without a stamp, as by an older gateway.
			stampedUsageEntry(usageIDUnstamped, "", 0),
		}))

		writes, err := reader.RecentWrites(ctx, 0, "writer-c", 10)
		require.NoError(t, err)
		assert.Equal(t, []storage.RecentWrite{
			{ID: usageIDNew, WrittenAt: 300},
			{ID: usageIDMid, WrittenAt: 200},
			{ID: usageIDOld, WrittenAt: 100},
		}, writes, "newest first, unstamped rows left out")

		writes, err = reader.RecentWrites(ctx, 200, "writer-c", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{usageIDNew, usageIDMid}, usageWriteIDs(writes), "since is inclusive")

		writes, err = reader.RecentWrites(ctx, 0, "writer-a", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{usageIDMid}, usageWriteIDs(writes), "the excluded writer's rows are left out")

		writes, err = reader.RecentWrites(ctx, 0, "writer-c", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{usageIDNew, usageIDMid}, usageWriteIDs(writes), "limit keeps the newest")
	})
}

func TestUsageLogEntriesByID(t *testing.T) {
	runRecentWritesSuite(t, func(t *testing.T, store UsageStore, reader RecentWriteReader) {
		ctx := context.Background()
		cost := 0.25
		withCost := stampedUsageEntry(usageIDOld, "writer-a", 100)
		withCost.TotalCost = &cost
		withCost.SessionID = "session-1"
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			withCost,
			stampedUsageEntry(usageIDMid, "writer-a", 100),
			stampedUsageEntry(usageIDNew, "writer-a", 100),
		}))

		entries, err := reader.LogEntriesByID(ctx, []string{usageIDOld, usageIDMid, usageIDMissing})
		require.NoError(t, err)
		require.Len(t, entries, 2)
		slices.SortFunc(entries, func(a, b UsageLogEntry) int { return strings.Compare(a.ID, b.ID) })
		assert.Equal(t, usageIDOld, entries[0].ID)
		assert.Equal(t, "req-"+usageIDOld, entries[0].RequestID)
		assert.Equal(t, "session-1", entries[0].SessionID)
		assert.Equal(t, 15, entries[0].TotalTokens)
		require.NotNil(t, entries[0].TotalCost)
		assert.InDelta(t, cost, *entries[0].TotalCost, 1e-9)
		assert.True(t, entries[0].Timestamp.Equal(withCost.Timestamp))
		assert.Equal(t, usageIDMid, entries[1].ID)

		entries, err = reader.LogEntriesByID(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

// A table created before the write stamp existed gains its columns on start,
// and its existing rows stay out of RecentWrites.
func TestUsageSQLStoreAddsWriteStampColumns(t *testing.T) {
	check := func(t *testing.T, db sqlx.DB, buildIndexes func()) {
		ctx := context.Background()
		require.NoError(t, ensureUsageSchema(ctx, db))
		// SQLite refuses to drop an indexed column.
		_, err := db.Exec(ctx, "DROP INDEX IF EXISTS "+writtenAtIndex)
		require.NoError(t, err)
		for _, column := range []string{"writer_id", "written_at"} {
			_, err = db.Exec(ctx, "ALTER TABLE usage DROP COLUMN "+column)
			require.NoError(t, err)
		}
		legacy := stampedUsageEntry(usageIDOld, "", 0)
		_, err = db.Exec(ctx,
			"INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, endpoint) VALUES (?, ?, ?, ?, ?, ?, ?)",
			legacy.ID, legacy.RequestID, legacy.ProviderID, db.Dialect().TimestampArg(legacy.Timestamp),
			legacy.Model, legacy.Provider, legacy.Endpoint)
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		buildIndexes()
		assert.True(t, hasValidUsageIndex(ctx, t, db, writtenAtIndex), "written_at index is built")

		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{stampedUsageEntry(usageIDNew, "writer-a", 100)}))
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		writes, err := reader.RecentWrites(ctx, 0, "writer-b", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{usageIDNew}, usageWriteIDs(writes))
	}

	t.Run("sqlite", func(t *testing.T) {
		check(t, sqlxtest.NewSQLite(t), func() {})
	})
	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return // skipped: no test server configured
		}
		db, err := sqlx.NewPostgreSQL(pool)
		require.NoError(t, err)
		check(t, db, func() {
			for _, index := range backgroundIndexes {
				require.True(t, tryBuildBackgroundIndex(context.Background(), pool, index), index.name)
			}
		})
	})
}

func hasValidUsageIndex(ctx context.Context, t *testing.T, db sqlx.DB, name string) bool {
	t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if db.Dialect() == sqlx.PostgreSQL {
		query = `SELECT COUNT(*) FROM pg_index WHERE indexrelid = to_regclass(?) AND indisvalid`
	}
	require.NoError(t, db.QueryRow(ctx, query, name).Scan(&count))
	return count == 1
}

// The logger stamps every entry of a flushed batch with this process's
// writer id and one write time.
func TestUsageLoggerStampsFlushedEntries(t *testing.T) {
	store := &mockStore{}
	logger := NewLogger(store, Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
	before := time.Now().UnixMicro()
	logger.Write(&UsageEntry{ID: "one"})
	logger.Write(&UsageEntry{ID: "two"})
	require.NoError(t, logger.Close())

	entries := store.getEntries()
	require.Len(t, entries, 2)
	for _, entry := range entries {
		require.NotNil(t, entry.Stamp)
		assert.Equal(t, storage.WriterID(), entry.Stamp.WriterID)
		assert.GreaterOrEqual(t, entry.Stamp.WrittenAt, before)
	}
	assert.Same(t, entries[0].Stamp, entries[1].Stamp, "one stamp per batch")
}
