package auditlog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These replace a pair of tests that only asserted the shape of a generated
// SQL string. The write path now executes against both engines instead.

func runSQLStoreTest(t *testing.T, retentionDays int, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, retentionDays)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store, db)
	})
}

func testLogEntry(id string, at time.Time) *LogEntry {
	return &LogEntry{
		ID:             id,
		Timestamp:      at,
		DurationNs:     1234,
		RequestedModel: "gpt-4o-mini",
		ResolvedModel:  "gpt-4o-mini",
		Provider:       "openai",
		ProviderName:   "primary-openai",
		AliasUsed:      true,
		CacheType:      CacheTypeExact,
		StatusCode:     200,
		RequestID:      "req-" + id,
		PrincipalID:    "oidc:principal-1",
		AuthKeyID:      "auth-key-1",
		AuthMethod:     "bearer",
		ClientIP:       "127.0.0.1",
		Method:         "POST",
		Path:           "/v1/chat/completions",
		UserPath:       "/team",
		Stream:         true,
		Data:           &LogData{UserAgent: "test-agent", Labels: []string{"team-a"}},
	}
}

func TestSQLStoreWriteBatchRoundTrip(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		err := store.WriteBatch(ctx, []*LogEntry{testLogEntry("log-1", now)})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		result, err := reader.GetLogs(ctx, LogQueryParams{Limit: 10, OmitAttempts: true})
		require.NoError(t, err)
		require.Len(t, result.Entries, 1)

		entry := result.Entries[0]
		assert.Equal(t, "gpt-4o-mini", entry.RequestedModel)
		assert.Equal(t, "/team", entry.UserPath)
		assert.Equal(t, 200, entry.StatusCode)
		assert.Equal(t, "oidc:principal-1", entry.PrincipalID)
		assert.Equal(t, "req-log-1", entry.RequestID)
		assert.Equal(t, "auth-key-1", entry.AuthKeyID)
		assert.Equal(t, "bearer", entry.AuthMethod)

		// Booleans round-trip as booleans on both engines, without an
		// int-conversion helper on either side.
		assert.True(t, entry.AliasUsed)
		assert.True(t, entry.Stream)
	})
}

func TestSQLStoreWriteBatchIgnoresDuplicateIDs(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		entry := testLogEntry("log-1", now)
		for range 2 {
			err := store.WriteBatch(ctx, []*LogEntry{entry})
			require.NoError(t, err)
		}

		// A retried flush must not fail or duplicate the row.
		var count int
		err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

func TestSQLStoreWriteBatchChunksBeyondParameterLimit(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()

		// More than one chunk: SQLite rejects a statement binding over 999
		// parameters, so the batch has to be split.
		total := maxEntriesPerBatch*2 + 3
		entries := make([]*LogEntry, 0, total)
		for i := range total {
			entries = append(entries, testLogEntry(fmt.Sprintf("log-%03d", i), now))
		}
		err := store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		var count int
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, total, count)
	})
}

func TestSQLStoreParameterLimitFitsOneChunk(t *testing.T) {
	require.LessOrEqual(t, maxEntriesPerBatch*columnsPerEntry, maxSQLParams)
}

func TestSQLStoreCleanupDropsEntriesPastRetention(t *testing.T) {
	runSQLStoreTest(t, 1, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Now().UTC()
		err := store.WriteBatch(ctx, []*LogEntry{
			testLogEntry("fresh", now),
			testLogEntry("stale", now.AddDate(0, 0, -3)),
		})
		require.NoError(t, err)

		store.cleanup()

		var count int
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE id = ?`, "stale").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE id = ?`, "fresh").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

func TestNewSQLStoreRenamesLegacyModelColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// The column was called `model` before requested/resolved were split.
		err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				model TEXT,
				status_code INTEGER DEFAULT 0
			)`)
		require.NoError(t, err)
		_, err = NewSQLStore(ctx, db, 0)
		require.NoError(t, err)

		columns, err := auditColumns(ctx, db, auditLogTable)
		require.NoError(t, err)

		assert.False(t, columns["model"], "legacy model column still present")
		assert.True(t, columns["requested_model"])
	})
}

func TestNewSQLStoreDropsLegacyExecutionPlanIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Databases created before v0.1.17 carry the pre-rename column and index.
		err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				execution_plan_version_id TEXT,
				status_code INTEGER DEFAULT 0
			)`,
			`CREATE INDEX idx_audit_execution_plan_version_id ON audit_logs(execution_plan_version_id)`,
		)
		require.NoError(t, err)
		_, err = NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		// Recreating the index must succeed, proving the startup drop removed it.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_execution_plan_version_id ON audit_logs(execution_plan_version_id)`)
		require.NoError(t, err)
	})
}

func TestNewSQLStoreReplacesSingleColumnUserPathIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Databases created before the composite index carry the single-column one.
		err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				user_path TEXT,
				status_code INTEGER DEFAULT 0
			)`,
			`CREATE INDEX idx_audit_user_path ON audit_logs(user_path)`,
		)
		require.NoError(t, err)
		_, err = NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		// Recreating the old index must succeed, proving the startup drop removed it.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_user_path ON audit_logs(user_path)`)
		require.NoError(t, err)
		// And the composite replacement must now exist.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_user_path_timestamp ON audit_logs(user_path, timestamp)`)
		require.Error(t, err)
	})
}

// The index must be declared on the same expression the reader compares
// against (readerDialect.userPath), or the planner cannot use it: on
// PostgreSQL that is the column under COLLATE "C".
func TestUserPathIndexesMatchReaderExpression(t *testing.T) {
	for _, dialect := range []sqlx.Dialect{sqlx.SQLite, sqlx.PostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			column := readerDialectFor(dialect).userPath
			want := []string{
				"DROP INDEX IF EXISTS idx_audit_user_path",
				"CREATE INDEX IF NOT EXISTS idx_audit_user_path_timestamp ON audit_logs(" + column + ", timestamp)",
			}
			require.Equal(t, want, userPathIndexes(dialect))
		})
	}
	require.Equal(t, `user_path COLLATE "C"`, readerDialectFor(sqlx.PostgreSQL).userPath)
}
