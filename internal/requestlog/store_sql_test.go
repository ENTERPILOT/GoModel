package requestlog

import (
	"context"
	"fmt"
	"sync"
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

// startSQLStore starts a store and waits for its background index builds, so
// DDL a test then runs on the request log tables does not meet one: a statement
// waiting for a table that an index is being built on deadlocks with the build.
func startSQLStore(t *testing.T, db sqlx.DB) {
	t.Helper()
	store, err := NewSQLStore(context.Background(), db, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.indexBuild.Wait()
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
		startSQLStore(t, db)

		columns, err := sqlx.Columns(ctx, db, requestLogTable)
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
		startSQLStore(t, db)
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
		startSQLStore(t, db)
		// Recreating the old index must succeed, proving the startup drop removed it.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_user_path ON audit_logs(user_path)`)
		require.NoError(t, err)
		// And the composite replacement must now exist.
		_, err = db.Exec(ctx, `CREATE INDEX idx_audit_user_path_timestamp ON audit_logs(user_path, timestamp)`)
		require.Error(t, err)
	})
}

// Replicas that start together against one database change the request log schema
// one at a time: each of them starts, whether the database is new or an older
// release created it, and every index gets built.
func TestNewSQLStoreStartsReplicasTogether(t *testing.T) {
	cases := map[string][]string{
		"new database": nil,
		"legacy schema": {`
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp ` + sqlx.TypeTimestamp + ` NOT NULL,
				model TEXT,
				status_code INTEGER DEFAULT 0
			)`},
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
				if db.Dialect() != sqlx.PostgreSQL {
					t.Skip("replicas share a database only on PostgreSQL")
				}
				ctx := context.Background()
				require.NoError(t, db.Schema(ctx, existing...))

				const replicas = 4
				stores := make([]*SQLStore, replicas)
				errs := make([]error, replicas)
				var starting sync.WaitGroup
				for i := range replicas {
					starting.Go(func() { stores[i], errs[i] = NewSQLStore(ctx, db, 0) })
				}
				starting.Wait()
				for _, store := range stores {
					if store != nil {
						t.Cleanup(func() { _ = store.Close() })
					}
				}
				for i, err := range errs {
					require.NoError(t, err, "replica %d", i)
				}
				for _, store := range stores {
					store.indexBuild.Wait()
				}

				for _, index := range append(modelIndexes(sqlx.PostgreSQL), requestLogIndex{name: writtenAtIndex}) {
					assert.True(t, hasValidIndex(ctx, t, db, index.name), index.name)
				}
				var invalid int
				require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM pg_index i
					JOIN pg_class c ON c.oid = i.indrelid
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE n.nspname = current_schema() AND NOT i.indisvalid`).Scan(&invalid))
				assert.Zero(t, invalid, "no build is left interrupted")
			})
		})
	}
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
			want := []requestLogIndex{
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
		entryCount := (maxAttemptsPerBatch*2)/attemptsPerEntry + 1
		require.LessOrEqual(t, entryCount, maxEntriesPerBatch, "the entries must fit one entry chunk, so attempt chunking is what is exercised")

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

		require.Greater(t, entryCount*attemptsPerEntry, maxAttemptsPerBatch*2, "more than two attempt chunks")
		require.NoError(t, store.WriteBatch(ctx, entries))

		var count int
		err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log_attempts`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, entryCount*attemptsPerEntry, count)
	})
}

func TestSQLStoreAttemptParameterLimitFitsOneChunk(t *testing.T) {
	require.LessOrEqual(t, maxAttemptsPerBatch*columnsPerAttempt, maxSQLParams)
}

// A chunk's entries and attempts commit together: when the attempts cannot be
// written, neither is the entry, so no reader sees it without them.
func TestSQLStoreWriteBatchCommitsEntriesWithTheirAttempts(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		// The drop needs a lock on audit_logs through the foreign key, and a
		// background index build still running there deadlocks with it.
		store.indexBuild.Wait()
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

// Replacing an interrupted build must leave audit_logs writable. A plain DROP
// INDEX waits for every open transaction that has touched the table, and the
// writes of the requests being logged queue behind it until that one ends.
func TestRebuildingInterruptedIndexKeepsRequestLogsWritable(t *testing.T) {
	model := modelIndexes(sqlx.PostgreSQL)[0]
	rebuilds := map[string]struct {
		index   string
		rebuild func(ctx context.Context, session sqlx.DB) error
	}{
		"model index": {model.name, func(ctx context.Context, session sqlx.DB) error {
			return buildIndexConcurrently(ctx, session, model)
		}},
		"search index": {trigramSearchIndex, func(ctx context.Context, session sqlx.DB) error {
			ensureTrigramSearchIndex(ctx, session, postgresErrorMessage)
			return nil
		}},
	}
	for name, tc := range rebuilds {
		t.Run(name, func(t *testing.T) {
			sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
				if db.Dialect() != sqlx.PostgreSQL {
					t.Skip("concurrent index builds are PostgreSQL only")
				}
				ctx := context.Background()
				store, err := NewSQLStore(ctx, db, 0)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				store.indexBuild.Wait()
				if !hasValidIndex(ctx, t, db, tc.index) {
					t.Skipf("%s is not built on this server", tc.index)
				}
				_, err = db.Exec(ctx, `UPDATE pg_index SET indisvalid = FALSE WHERE indexrelid = to_regclass(?)`, tc.index)
				if err != nil {
					t.Skipf("cannot mark the index invalid on this server: %v", err)
				}

				// A long-running reader: an open transaction that has read the table.
				pool := sqlx.PostgreSQLPool(db)
				reader, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = reader.Rollback(ctx) }()
				_, err = reader.Exec(ctx, `SELECT 1 FROM audit_logs LIMIT 1`)
				require.NoError(t, err)

				conn, err := pool.Acquire(ctx)
				require.NoError(t, err)
				defer conn.Release()
				var rebuilder int
				require.NoError(t, conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&rebuilder))
				session, err := sqlx.NewPostgreSQLConn(conn)
				require.NoError(t, err)
				rebuilt := make(chan error, 1)
				go func() { rebuilt <- tc.rebuild(ctx, session) }()

				// The drop waits for the reader either way; what matters is
				// whether a write can pass it meanwhile.
				require.Eventually(t, func() bool {
					var waiting bool
					err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = ? AND NOT granted)`, rebuilder).Scan(&waiting)
					return err == nil && waiting
				}, 10*time.Second, 10*time.Millisecond, "the rebuild waits for the reader")

				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				require.NoError(t, store.WriteBatch(writeCtx, []*LogEntry{testLogEntry("during-rebuild", time.Now())}),
					"a request is logged while the rebuild waits")

				require.NoError(t, reader.Rollback(ctx))
				require.NoError(t, <-rebuilt)
				assert.True(t, hasValidIndex(ctx, t, db, tc.index))
			})
		})
	}
}
