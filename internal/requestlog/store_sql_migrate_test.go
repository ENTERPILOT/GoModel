package requestlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyNames spells a statement the way v0.2.5 did: that release created the
// schema this one creates, under the audit_logs names.
var legacyNames = strings.NewReplacer(
	attemptsTable, legacyAttemptsTable,
	attemptsLogID, legacyAttemptsLogID,
	requestLogTable, legacyRequestLogTable,
	attemptIndexPrefix, legacyAttemptIndexes,
	requestLogIndexPrefix, legacyIndexPrefix,
)

// createLegacyTables applies DDL written with the current names under the
// names v0.2.5 and older used. Every database those releases created has
// them, so a fixture of an older schema is only faithful with them.
func createLegacyTables(ctx context.Context, db sqlx.DB, statements ...string) error {
	legacy := make([]string, len(statements))
	for i, statement := range statements {
		legacy[i] = legacyNames.Replace(statement)
	}
	return db.Schema(ctx, legacy...)
}

// createLegacySchema builds the tables and startup indexes of v0.2.5, plus one
// index that release had already retired but an older database may still hold.
func createLegacySchema(t *testing.T, db sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, createLegacyTables(ctx, db, sqlTables...))

	indexes := append(append([]string{}, sqlIndexes...), userPathIndexes(db.Dialect())...)
	indexes = append(indexes, jsonPathIndexes(db.Dialect())...)
	indexes = append(indexes, "CREATE INDEX idx_request_logs_session_id ON request_logs(session_id)")
	for _, statement := range indexes {
		_, err := db.Exec(ctx, legacyNames.Replace(statement))
		require.NoError(t, err, statement)
	}
}

// writeLegacyEntry inserts one entry and one provider attempt with the
// statements v0.2.5 writes them with.
func writeLegacyEntry(t *testing.T, db sqlx.DB, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	dialect := db.Dialect()
	entry := testLogEntry(id, at)
	attempt := AttemptSnapshot{Seq: 1, Kind: AttemptKindFailover, ProviderType: "openai", StatusCode: 503, StartedAt: at}

	require.NoError(t, db.InTx(ctx, func(q sqlx.Querier) error {
		insert := legacyNames.Replace(insertRequestLogPrefix) + requestLogRowPlaceholder + ` ON CONFLICT (id) DO NOTHING`
		if _, err := q.Exec(ctx, insert, requestLogValues(dialect, entry)...); err != nil {
			return err
		}
		insert = legacyNames.Replace(insertAttemptPrefix + attemptRowPlaceholder + insertAttemptSuffix)
		_, err := q.Exec(ctx, insert, requestLogAttemptValues(dialect, id, attempt)...)
		return err
	}))
}

func relationKindOf(t *testing.T, db sqlx.DB, name string) string {
	t.Helper()
	kind, err := relationKind(context.Background(), db, db.Dialect(), name)
	require.NoError(t, err)
	return kind
}

// indexNames lists every index on the request log tables.
func indexNames(t *testing.T, db sqlx.DB) []string {
	t.Helper()
	query := `SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`
	if db.Dialect() == sqlx.PostgreSQL {
		query = `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()`
	}
	names, err := queryStrings(context.Background(), db, query)
	require.NoError(t, err)
	return names
}

func readEntry(t *testing.T, db sqlx.DB, id string) *LogEntry {
	t.Helper()
	reader, err := NewSQLReader(db)
	require.NoError(t, err)
	entry, err := reader.GetLogByID(context.Background(), id)
	require.NoError(t, err)
	return entry
}

func TestNewSQLStoreRenamesLegacyTables(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		at := time.Unix(1700000000, 0).UTC()
		createLegacySchema(t, db)
		writeLegacyEntry(t, db, "written-before", at)

		// Twice: the second start finds the work done.
		startSQLStore(t, db)
		startSQLStore(t, db)

		entry := readEntry(t, db, "written-before")
		require.NotNil(t, entry)
		require.NotNil(t, entry.Data)
		require.Len(t, entry.Data.Attempts, 1, "attempts follow their entry")
		assert.Equal(t, 503, entry.Data.Attempts[0].StatusCode)

		for _, name := range indexNames(t, db) {
			assert.NotContains(t, name, "audit", "index kept a legacy name")
		}
		names := indexNames(t, db)
		assert.Contains(t, names, "idx_request_logs_timestamp")
		assert.Contains(t, names, "idx_request_log_attempts_log_seq")
		assert.NotContains(t, names, "idx_request_logs_session_id", "the retired index is still dropped")

		wantLegacy := ""
		if db.Dialect() == sqlx.PostgreSQL {
			wantLegacy = relationView
		}
		assert.Equal(t, wantLegacy, relationKindOf(t, db, legacyRequestLogTable))
		assert.Equal(t, wantLegacy, relationKindOf(t, db, legacyAttemptsTable))

		// The foreign key followed both renames. SQLite does not enforce it
		// (the gateway leaves foreign_keys off), so its definition is checked.
		if db.Dialect() == sqlx.SQLite {
			definitions, err := queryStrings(ctx, db, `SELECT sql FROM sqlite_master WHERE sql IS NOT NULL`)
			require.NoError(t, err)
			for _, definition := range definitions {
				assert.NotContains(t, definition, "audit")
			}
			return
		}
		_, err := db.Exec(ctx, `DELETE FROM request_logs WHERE id = ?`, "written-before")
		require.NoError(t, err)
		var attempts int
		require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM request_log_attempts`).Scan(&attempts))
		assert.Zero(t, attempts)
	})
}

// A PostgreSQL schema an upgrade renamed must be the schema a fresh database
// gets, down to the names PostgreSQL derives itself.
func TestNewSQLStoreRenamedSchemaMatchesFresh(t *testing.T) {
	names := func(t *testing.T, db sqlx.DB) []string {
		t.Helper()
		found, err := queryStrings(context.Background(), db, `
			SELECT relname::text FROM pg_class
				WHERE relnamespace = current_schema()::regnamespace AND relkind IN ('r', 'i', 'S')
			UNION ALL
			SELECT conname::text FROM pg_constraint WHERE connamespace = current_schema()::regnamespace
			ORDER BY 1`)
		require.NoError(t, err)
		return found
	}

	var fresh, renamed []string
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("PostgreSQL only")
		}
		startSQLStore(t, db)
		fresh = names(t, db)
	})
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("PostgreSQL only")
		}
		createLegacySchema(t, db)
		startSQLStore(t, db)
		renamed = names(t, db)
	})
	if fresh == nil {
		t.Skip("no PostgreSQL test server")
	}
	assert.Equal(t, fresh, renamed)
}

// During a rolling upgrade a replica on v0.2.5 keeps running, and may restart,
// against the renamed PostgreSQL schema. Its statements go through the views.
func TestLegacyViewsServeTheOlderRelease(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("only PostgreSQL runs more than one gateway on a database")
		}
		ctx := context.Background()
		at := time.Unix(1700000000, 0).UTC()
		createLegacySchema(t, db)
		startSQLStore(t, db)

		// Its startup: the tables "exist", and no column is missing.
		require.NoError(t, createLegacyTables(ctx, db, sqlTables...))
		var legacyMigrations []string
		for _, statement := range sqlMigrations {
			legacyMigrations = append(legacyMigrations, legacyNames.Replace(statement))
		}
		require.NoError(t, sqlx.AddColumns(ctx, db, legacyMigrations...))

		// Its writes, twice over to hit both ON CONFLICT clauses.
		writeLegacyEntry(t, db, "from-older-replica", at)
		writeLegacyEntry(t, db, "from-older-replica", at)

		entry := readEntry(t, db, "from-older-replica")
		require.NotNil(t, entry, "the upgraded replicas see the entry")
		require.Len(t, entry.Data.Attempts, 1)

		// Its reads and its retention sweep.
		var joined int
		require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs l
			JOIN audit_log_attempts a ON a.audit_log_id = l.id WHERE l.id = ?`, "from-older-replica").Scan(&joined))
		assert.Equal(t, 1, joined)

		_, err := db.Exec(ctx, `DELETE FROM audit_log_attempts WHERE audit_log_id IN (SELECT id FROM audit_logs WHERE timestamp < ?)`,
			db.Dialect().TimestampArg(at.Add(time.Hour)))
		require.NoError(t, err)
		_, err = db.Exec(ctx, `DELETE FROM audit_logs WHERE timestamp < ?`, db.Dialect().TimestampArg(at.Add(time.Hour)))
		require.NoError(t, err)
		assert.Nil(t, readEntry(t, db, "from-older-replica"))
	})
}

// A binary rolled back to v0.2.5 on a database that has no legacy tables (or,
// on SQLite, none any more) creates them again and logs there. The next
// upgrade moves those entries over.
func TestNewSQLStoreAbsorbsTablesARollbackRecreated(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		at := time.Unix(1700000000, 0).UTC()

		store, err := NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		store.indexBuild.Wait()
		kept := testLogEntry("in-both", at)
		kept.StatusCode = 200
		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{testLogEntry("written-before", at), kept}))
		require.NoError(t, store.Close())

		createLegacySchema(t, db)
		writeLegacyEntry(t, db, "written-during-rollback", at.Add(time.Minute))
		writeLegacyEntry(t, db, "in-both", at)
		_, err = db.Exec(ctx, `UPDATE audit_logs SET status_code = 500 WHERE id = ?`, "in-both")
		require.NoError(t, err)

		startSQLStore(t, db)

		for _, id := range []string{"written-before", "written-during-rollback", "in-both"} {
			require.NotNil(t, readEntry(t, db, id), id)
		}
		moved := readEntry(t, db, "written-during-rollback")
		require.Len(t, moved.Data.Attempts, 1, "attempts move with their entry")
		assert.Equal(t, 200, readEntry(t, db, "in-both").StatusCode, "an entry already present is left alone")

		wantLegacy := ""
		if db.Dialect() == sqlx.PostgreSQL {
			wantLegacy = relationView
		}
		assert.Equal(t, wantLegacy, relationKindOf(t, db, legacyRequestLogTable))
		assert.Equal(t, wantLegacy, relationKindOf(t, db, legacyAttemptsTable))
	})
}

// With request logging turned off no store is created, and the readers still
// have to find what an older release logged.
func TestReaderFindsLegacyTablesWithoutAStore(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		createLegacySchema(t, db)
		writeLegacyEntry(t, db, "written-before", time.Unix(1700000000, 0).UTC())

		reader, err := newAdoptingSQLReader(db)
		require.NoError(t, err)
		entry, err := reader.GetLogByID(context.Background(), "written-before")
		require.NoError(t, err)
		require.NotNil(t, entry)
		assert.Len(t, entry.Data.Attempts, 1)
	})
}

func TestNewSQLStoreLeavesAFreshDatabaseWithoutLegacyNames(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, _ *SQLStore, db sqlx.DB) {
		assert.Empty(t, relationKindOf(t, db, legacyRequestLogTable))
		assert.Empty(t, relationKindOf(t, db, legacyAttemptsTable))
	})
}

// The views are made when the tables are renamed, before this release brings
// the tables up to date. A database from before the attempts table, or from
// before a column v0.2.5 expects, must still end up with views that binary can
// start on: it would otherwise try to create a table that references a view,
// or to add a column to one.
func TestNewSQLStoreBringsLegacyViewsUpToDate(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("only PostgreSQL keeps views under the legacy names")
		}
		ctx := context.Background()
		// The entry table alone, before it had its write stamp columns.
		table, removed := sqlTables[0], 0
		for _, column := range []string{"writer_id TEXT", "written_at " + sqlx.TypeInt64} {
			if i := strings.Index(table, column); i >= 0 {
				table = strings.TrimRight(table[:i], ", \t\n") + table[i+len(column):]
				removed++
			}
		}
		require.Equal(t, 2, removed, "the fixture no longer matches the table")
		require.NoError(t, createLegacyTables(ctx, db, table))
		startSQLStore(t, db)

		views, err := legacyViewsCurrent(ctx, db)
		require.NoError(t, err)
		assert.True(t, views, "the views show every column of their tables")
		assert.Equal(t, relationView, relationKindOf(t, db, legacyAttemptsTable))

		// The startup and the writes of v0.2.5.
		require.NoError(t, createLegacyTables(ctx, db, sqlTables...))
		var legacyMigrations []string
		for _, statement := range sqlMigrations {
			legacyMigrations = append(legacyMigrations, legacyNames.Replace(statement))
		}
		require.NoError(t, sqlx.AddColumns(ctx, db, legacyMigrations...))
		writeLegacyEntry(t, db, "from-older-replica", time.Unix(1700000000, 0).UTC())
		require.NotNil(t, readEntry(t, db, "from-older-replica"))

		// A second start finds the views current and leaves them alone.
		startSQLStore(t, db)
		views, err = legacyViewsCurrent(ctx, db)
		require.NoError(t, err)
		assert.True(t, views)
	})
}

// A start can stop between renaming the entry table and creating the attempts
// table, on a database old enough to have had none. If an older binary then
// recreates the legacy tables, the next start must first complete the request
// log tables and only then move the rows over.
func TestNewSQLStoreAbsorbsAfterAnInterruptedRename(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		require.NoError(t, db.Schema(ctx, sqlTables[0]))
		createLegacySchema(t, db)
		writeLegacyEntry(t, db, "written-during-rollback", time.Unix(1700000000, 0).UTC())

		// A reader has no attempts table to move into and leaves the rows be.
		_, err := newAdoptingSQLReader(db)
		require.NoError(t, err)
		assert.Equal(t, relationTable, relationKindOf(t, db, legacyRequestLogTable))

		startSQLStore(t, db)

		entry := readEntry(t, db, "written-during-rollback")
		require.NotNil(t, entry)
		require.Len(t, entry.Data.Attempts, 1)
		assert.NotEqual(t, relationTable, relationKindOf(t, db, legacyRequestLogTable))
		assert.NotEqual(t, relationTable, relationKindOf(t, db, legacyAttemptsTable))
	})
}

// With the request log tables short of a column the legacy ones hold, moving
// the rows would drop what it holds. A reader, which cannot migrate the
// tables, leaves the rows where they are; the store migrates and then moves.
func TestReaderLeavesLegacyRowsItCannotFullyMove(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		tables := make([]string, len(sqlTables))
		copy(tables, sqlTables)
		short := strings.Replace(tables[1], "response_body TEXT,", "", 1)
		require.NotEqual(t, tables[1], short, "the fixture no longer matches the table")
		tables[1] = short
		require.NoError(t, db.Schema(ctx, tables...))
		createLegacySchema(t, db)
		writeLegacyEntry(t, db, "written-during-rollback", time.Unix(1700000000, 0).UTC())
		_, err := db.Exec(ctx, `UPDATE audit_log_attempts SET response_body = ?`, `{"kept":true}`)
		require.NoError(t, err)

		_, err = newAdoptingSQLReader(db)
		require.NoError(t, err)
		assert.Equal(t, relationTable, relationKindOf(t, db, legacyAttemptsTable), "the reader left the rows in place")

		startSQLStore(t, db)
		var body string
		require.NoError(t, db.QueryRow(ctx, `SELECT response_body FROM request_log_attempts`).Scan(&body))
		assert.JSONEq(t, `{"kept":true}`, body)
	})
}

// A replica on the older release can be writing the recreated legacy tables
// while they are absorbed. Its write must not fall between the copy and the
// drop.
func TestNewSQLStoreAbsorbWaitsForALegacyWriter(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("only PostgreSQL runs more than one gateway on a database")
		}
		ctx := context.Background()
		startSQLStore(t, db)
		createLegacySchema(t, db)

		// The older replica has inserted and not committed yet.
		inserted, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			written <- db.InTx(ctx, func(q sqlx.Querier) error {
				entry := testLogEntry("committed-during-absorb", time.Unix(1700000000, 0).UTC())
				insert := legacyNames.Replace(insertRequestLogPrefix) + requestLogRowPlaceholder
				if _, err := q.Exec(ctx, insert, requestLogValues(db.Dialect(), entry)...); err != nil {
					return err
				}
				close(inserted)
				<-release
				return nil
			})
		}()
		<-inserted

		started := make(chan error, 1)
		go func() {
			store, err := NewSQLStore(ctx, db, 0)
			if err == nil {
				store.indexBuild.Wait()
				err = store.Close()
			}
			started <- err
		}()
		select {
		case err := <-started:
			t.Fatalf("the start did not wait for the legacy writer: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		require.NoError(t, <-written)
		require.NoError(t, <-started)

		require.NotNil(t, readEntry(t, db, "committed-during-absorb"))
	})
}
