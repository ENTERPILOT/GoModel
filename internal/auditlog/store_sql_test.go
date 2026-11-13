package auditlog

import (
	"context"
	"fmt"
	"strings"
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

		// More than one chunk: each engine rejects a statement binding past
		// its parameter limit, so the batch has to be split.
		total := rowLimitsFor(db.Dialect()).entries*2 + 3
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

func TestRowLimitsFitParameterLimit(t *testing.T) {
	tests := []struct {
		dialect      sqlx.Dialect
		maxParams    int
		wantEntries  int
		wantAttempts int
	}{
		{sqlx.SQLite, 999, 39, 66},
		{sqlx.PostgreSQL, 65535, 2621, 4369},
	}
	for _, tt := range tests {
		t.Run(string(tt.dialect), func(t *testing.T) {
			limits := rowLimitsFor(tt.dialect)
			assert.Equal(t, tt.wantEntries, limits.entries)
			assert.Equal(t, tt.wantAttempts, limits.attempts)
			assert.LessOrEqual(t, limits.entries*columnsPerEntry, tt.maxParams)
			assert.LessOrEqual(t, limits.attempts*columnsPerAttempt, tt.maxParams)
		})
	}
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

		columns, err := sqlx.Columns(ctx, db, auditLogTable)
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

// Like the user-path indexes, the model indexes must be declared on the
// expressions the reader's prefix ranges compare against.
func TestModelIndexesMatchReaderExpressions(t *testing.T) {
	for _, dialect := range []sqlx.Dialect{sqlx.SQLite, sqlx.PostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			reader := readerDialectFor(dialect)
			want := []auditIndex{
				{name: "idx_audit_requested_model_timestamp", columns: reader.requestedModel + ", timestamp"},
				{name: "idx_audit_resolved_model_timestamp", columns: reader.resolvedModel + ", timestamp"},
			}
			require.Equal(t, want, modelIndexes(dialect))
		})
	}
	require.Equal(t, `requested_model COLLATE "C"`, readerDialectFor(sqlx.PostgreSQL).requestedModel)
	require.Equal(t, `resolved_model COLLATE "C"`, readerDialectFor(sqlx.PostgreSQL).resolvedModel)
}

func TestNewSQLStoreReplacesSingleColumnRequestedModelIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				requested_model TEXT,
				status_code INTEGER DEFAULT 0
			)`,
			`CREATE INDEX idx_audit_requested_model ON audit_logs(requested_model)`,
		)
		require.NoError(t, err)
		store, err := NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		// PostgreSQL builds the replacements in the background, then drops the old index.
		store.indexBuild.Wait()
		// Recreating the old index must succeed, proving the drop removed it.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_requested_model ON audit_logs(requested_model)`)
		require.NoError(t, err)
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_resolved_model_timestamp ON audit_logs(resolved_model, timestamp)`)
		require.Error(t, err)
	})
}

// Attempt rows are chunked on their own column count: a failover-heavy batch
// carries several per entry, so the attempts of a single entry chunk can go
// well past the parameter limit on their own.
func TestSQLStoreWriteBatchChunksAttemptsBeyondParameterLimit(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()

		const attemptsPerEntry = 8
		limits := rowLimitsFor(db.Dialect())
		entryCount := (limits.attempts*2)/attemptsPerEntry + 1
		require.LessOrEqual(t, entryCount, limits.entries, "the entries must fit one entry chunk, so attempt chunking is what is exercised")

		entries := make([]*LogEntry, 0, entryCount)
		for i := range entryCount {
			entry := testLogEntry(fmt.Sprintf("attempts-%03d", i), now)
			attempts := make([]AttemptSnapshot, 0, attemptsPerEntry)
			for seq := range attemptsPerEntry {
				attempts = append(attempts, AttemptSnapshot{
					Seq:          seq + 1,
					Kind:         AttemptKindFailover,
					ProviderType: "openai",
					Model:        "openai/gpt-5.5",
					StatusCode:   503,
					ErrorType:    "server_error",
				})
			}
			if entry.Data == nil {
				entry.Data = &LogData{}
			}
			entry.Data.Attempts = attempts
			entries = append(entries, entry)
		}

		require.Greater(t, entryCount*attemptsPerEntry, limits.attempts*2, "more than two attempt chunks")
		require.NoError(t, store.WriteBatch(ctx, entries))

		var count int
		err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log_attempts`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, entryCount*attemptsPerEntry, count)
	})
}

// A chunk's entries and attempts commit together: when the attempts cannot be
// written, neither is the entry, so no reader sees it without them.
func TestSQLStoreWriteBatchCommitsEntriesWithTheirAttempts(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		_, err := db.Exec(ctx, `DROP TABLE audit_log_attempts`)
		require.NoError(t, err)

		withAttempt := testLogEntry("with-attempt", now)
		withAttempt.Data.Attempts = []AttemptSnapshot{{Seq: 1, Kind: AttemptKindFailover, ProviderType: "openai", StatusCode: 503}}
		require.Error(t, store.WriteBatch(ctx, []*LogEntry{withAttempt}))

		var count int
		require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count))
		assert.Zero(t, count, "the entry is rolled back with its attempts")
	})
}

// countingDB counts the transactions and statements a store runs through it.
type countingDB struct {
	sqlx.DB
	txs, execs int
}

func (c *countingDB) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	c.execs++
	return c.DB.Exec(ctx, query, args...)
}

func (c *countingDB) InTx(ctx context.Context, fn func(sqlx.Querier) error) error {
	c.txs++
	return c.DB.InTx(ctx, func(q sqlx.Querier) error {
		return fn(countingQuerier{Querier: q, db: c})
	})
}

type countingQuerier struct {
	sqlx.Querier
	db *countingDB
}

func (q countingQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	q.db.execs++
	return q.Querier.Exec(ctx, query, args...)
}

func countStatements(t *testing.T, store *SQLStore) *countingDB {
	t.Helper()
	counting := &countingDB{DB: store.db}
	store.db = counting
	return counting
}

// A whole flush commits once, in as few statements as the engine's parameter
// limit allows.
func TestSQLStoreWriteBatchWritesFlushInOneTransaction(t *testing.T) {
	for _, total := range []int{1, BatchFlushThreshold, 1000} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
				counting := countStatements(t, store)
				now := time.Unix(1700000000, 0).UTC()
				entries := make([]*LogEntry, total)
				attempts := 0
				for i := range entries {
					entries[i] = testLogEntry(fmt.Sprintf("log-%04d", i), now)
					if i%10 == 0 {
						entries[i].Data.Attempts = []AttemptSnapshot{{Seq: 1, Kind: AttemptKindFailover, ProviderType: "openai", StatusCode: 503}}
						attempts++
					}
				}
				require.NoError(t, store.WriteBatch(context.Background(), entries))

				limits := rowLimitsFor(db.Dialect())
				ceilDiv := func(n, d int) int { return (n + d - 1) / d }
				assert.Equal(t, 1, counting.txs)
				assert.Equal(t, ceilDiv(total, limits.entries)+ceilDiv(attempts, limits.attempts), counting.execs)

				var count int
				require.NoError(t, db.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_logs`).Scan(&count))
				assert.Equal(t, total, count)
			})
		})
	}
}

// Captured bodies count toward a statement's size: rows that fit the
// parameter limit still go to separate statements once their text would pass
// maxStatementBytes.
func TestSQLStoreWriteBatchCapsStatementBytes(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		counting := countStatements(t, store)
		now := time.Unix(1700000000, 0).UTC()
		large := strings.Repeat("a", maxStatementBytes/2)
		entries := make([]*LogEntry, 3)
		for i := range entries {
			entries[i] = testLogEntry(fmt.Sprintf("large-%d", i), now)
			entries[i].Data.UserAgent = large
		}
		require.NoError(t, store.WriteBatch(context.Background(), entries))

		assert.Equal(t, 1, counting.txs)
		assert.Equal(t, len(entries), counting.execs, "each entry passes half the cap, so no two share a statement")

		var count int
		require.NoError(t, db.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_logs`).Scan(&count))
		assert.Equal(t, len(entries), count)
	})
}

// rejectRequestID makes the database reject any further entry carrying the
// request id of an entry it already holds.
func rejectRequestID(t *testing.T, store *SQLStore, db sqlx.DB, existing *LogEntry) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, []*LogEntry{existing}))
	_, err := db.Exec(ctx, `CREATE UNIQUE INDEX test_unique_request_id ON audit_logs(request_id)`)
	require.NoError(t, err)
}

// One entry the database rejects fails the flush transaction; the retry writes
// every other entry on its own, so the bad entry costs only itself.
func TestSQLStoreWriteBatchRetriesEntriesAfterFailedFlush(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		existing := testLogEntry("existing", now)
		rejectRequestID(t, store, db, existing)
		counting := countStatements(t, store)

		entries := make([]*LogEntry, 5)
		for i := range entries {
			entries[i] = testLogEntry(fmt.Sprintf("log-%d", i), now)
		}
		entries[2].RequestID = existing.RequestID

		err := store.WriteBatch(ctx, entries)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to write 1 of 5 audit log entries")
		assert.Equal(t, 1+len(entries), counting.txs, "the flush, then one per entry")

		var count int
		require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count))
		assert.Equal(t, len(entries), count, "the existing entry and every new one but the rejected one")
	})
}

// When entries keep failing on their own, the database is failing every
// write: the retry stops instead of trying each remaining entry.
func TestSQLStoreWriteBatchStopsRetryAfterConsecutiveFailures(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		existing := testLogEntry("existing", now)
		rejectRequestID(t, store, db, existing)
		counting := countStatements(t, store)

		entries := make([]*LogEntry, 6)
		for i := range entries {
			entries[i] = testLogEntry(fmt.Sprintf("log-%d", i), now)
			entries[i].RequestID = existing.RequestID
		}

		err := store.WriteBatch(ctx, entries)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to write 6 of 6 audit log entries")
		assert.Equal(t, 1+maxConsecutiveRetryFailures, counting.txs)
	})
}

// A flush that fails because its context is done is not retried entry by
// entry: every retry would fail the same way.
func TestSQLStoreWriteBatchSkipsRetryOnDoneContext(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		counting := countStatements(t, store)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		now := time.Unix(1700000000, 0).UTC()
		entries := []*LogEntry{testLogEntry("log-0", now), testLogEntry("log-1", now), testLogEntry("log-2", now)}
		require.Error(t, store.WriteBatch(ctx, entries))
		assert.Equal(t, 1, counting.txs, "only the flush transaction")
	})
}

// An interrupted CREATE INDEX CONCURRENTLY leaves an invalid index; the next
// start must replace it rather than keep the unusable one.
func TestBuildIndexConcurrentlyRebuildsInvalidIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("concurrent index builds are PostgreSQL only")
		}
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		store.indexBuild.Wait()

		index := modelIndexes(sqlx.PostgreSQL)[0]
		_, err = db.Exec(ctx, `UPDATE pg_index SET indisvalid = FALSE WHERE indexrelid = to_regclass(?)`, index.name)
		if err != nil {
			t.Skipf("cannot mark the index invalid on this server: %v", err)
		}
		require.NoError(t, buildIndexConcurrently(ctx, db, index))

		var valid bool
		require.NoError(t, db.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass(?)`, index.name).Scan(&valid))
		assert.True(t, valid)
	})
}
