package auditlog

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func stampedEntry(id, writer string, writtenAt int64) *LogEntry {
	return &LogEntry{
		ID:             id,
		RequestID:      "req-" + id,
		Timestamp:      time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		RequestedModel: "gpt-5",
		Provider:       "openai",
		StatusCode:     200,
		Stamp:          stampOf(writer, writtenAt),
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

func writeIDs(writes []storage.RecentWrite) []string {
	ids := make([]string, len(writes))
	for i, write := range writes {
		ids[i] = write.ID
	}
	return ids
}

func TestRecentWrites(t *testing.T) {
	runReaderSuite(t, func(t *testing.T, store LogStore, reader Reader) {
		ctx := context.Background()
		recent, ok := reader.(RecentWriteReader)
		require.True(t, ok, "%T does not implement RecentWriteReader", reader)

		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{
			stampedEntry("a-old", "writer-a", 100),
			stampedEntry("b-mid", "writer-b", 200),
			stampedEntry("a-new", "writer-a", 300),
			// Written without a stamp, as by an older gateway.
			stampedEntry("unstamped", "", 0),
		}))

		writes, err := recent.RecentWrites(ctx, 0, "writer-c", 10)
		require.NoError(t, err)
		assert.Equal(t, []storage.RecentWrite{
			{ID: "a-new", WrittenAt: 300},
			{ID: "b-mid", WrittenAt: 200},
			{ID: "a-old", WrittenAt: 100},
		}, writes, "newest first, unstamped rows left out")

		writes, err = recent.RecentWrites(ctx, 200, "writer-c", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"a-new", "b-mid"}, writeIDs(writes), "since is inclusive")

		writes, err = recent.RecentWrites(ctx, 0, "writer-a", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"b-mid"}, writeIDs(writes), "the excluded writer's rows are left out")

		writes, err = recent.RecentWrites(ctx, 0, "writer-c", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"a-new", "b-mid"}, writeIDs(writes), "limit keeps the newest")
	})
}

func TestEntriesByID(t *testing.T) {
	runReaderSuite(t, func(t *testing.T, store LogStore, reader Reader) {
		ctx := context.Background()
		recent, ok := reader.(RecentWriteReader)
		require.True(t, ok, "%T does not implement RecentWriteReader", reader)

		withAttempt := stampedEntry("with-attempt", "writer-a", 100)
		withAttempt.Data = &LogData{
			UserAgent: "test-agent",
			Attempts: []AttemptSnapshot{{
				Seq:          1,
				Kind:         AttemptKindPrimary,
				ProviderType: "openai",
				Model:        "gpt-5",
				StatusCode:   200,
				Success:      true,
			}},
		}
		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{
			withAttempt,
			stampedEntry("plain", "writer-a", 100),
			stampedEntry("not-asked", "writer-a", 100),
		}))

		entries, err := recent.EntriesByID(ctx, []string{"with-attempt", "plain", "missing"})
		require.NoError(t, err)
		require.Len(t, entries, 2)
		slices.SortFunc(entries, func(a, b LogEntry) int { return strings.Compare(a.ID, b.ID) })
		assert.Equal(t, "plain", entries[0].ID)
		require.Equal(t, "with-attempt", entries[1].ID)
		assert.Equal(t, "req-with-attempt", entries[1].RequestID)
		assert.True(t, entries[1].Timestamp.Equal(withAttempt.Timestamp))
		require.NotNil(t, entries[1].Data)
		assert.Equal(t, "test-agent", entries[1].Data.UserAgent)
		require.Len(t, entries[1].Data.Attempts, 1, "provider attempts are hydrated")
		assert.Equal(t, "openai", entries[1].Data.Attempts[0].ProviderType)

		entries, err = recent.EntriesByID(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

// A table created before the write stamp existed gains its columns on start,
// and its existing rows stay out of RecentWrites.
func TestSQLStoreAddsWriteStampColumns(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		store.indexBuild.Wait()
		require.NoError(t, store.Close())

		// SQLite refuses to drop an indexed column.
		_, err = db.Exec(ctx, "DROP INDEX IF EXISTS "+writtenAtIndex)
		require.NoError(t, err)
		for _, column := range []string{"writer_id", "written_at"} {
			_, err = db.Exec(ctx, "ALTER TABLE audit_logs DROP COLUMN "+column)
			require.NoError(t, err)
		}
		_, err = db.Exec(ctx, "INSERT INTO audit_logs (id, timestamp) VALUES (?, ?)",
			"legacy", db.Dialect().TimestampArg(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
		require.NoError(t, err)

		store, err = newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		store.indexBuild.Wait()

		columns, err := auditColumns(ctx, db, auditLogTable)
		require.NoError(t, err)
		assert.True(t, columns["writer_id"])
		assert.True(t, columns["written_at"])
		assert.True(t, hasValidIndex(ctx, t, db, writtenAtIndex), "written_at index is built")

		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{stampedEntry("stamped", "writer-a", 100)}))
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		writes, err := reader.RecentWrites(ctx, 0, "writer-b", 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"stamped"}, writeIDs(writes))
	})
}

// The logger stamps every entry of a flushed batch with this process's
// writer id and one write time.
func TestLoggerStampsFlushedEntries(t *testing.T) {
	store := &mockStore{}
	logger := NewLogger(store, Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
	before := time.Now().UnixMicro()
	logger.Write(&LogEntry{ID: "one"})
	logger.Write(&LogEntry{ID: "two"})
	require.NoError(t, logger.Close())

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.entries, 2)
	for _, entry := range store.entries {
		require.NotNil(t, entry.Stamp)
		assert.Equal(t, storage.WriterID(), entry.Stamp.WriterID)
		assert.GreaterOrEqual(t, entry.Stamp.WrittenAt, before)
	}
	assert.Same(t, store.entries[0].Stamp, store.entries[1].Stamp, "one stamp per batch")
}

func hasValidIndex(ctx context.Context, t *testing.T, db sqlx.DB, name string) bool {
	t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if db.Dialect() == sqlx.PostgreSQL {
		query = `SELECT COUNT(*) FROM pg_index WHERE indexrelid = to_regclass(?) AND indisvalid`
	}
	require.NoError(t, db.QueryRow(ctx, query, name).Scan(&count))
	return count == 1
}
