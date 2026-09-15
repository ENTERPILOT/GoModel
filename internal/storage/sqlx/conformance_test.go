package sqlx_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conformance suite pins the behaviour store code is allowed to rely on.
// Anything asserted here must hold identically on every dialect, because a
// single store implementation now runs on all of them.

const conformanceSchema = `
	CREATE TABLE IF NOT EXISTS conformance (
		id TEXT PRIMARY KEY,
		flag ` + sqlx.TypeBool + ` NOT NULL,
		doc ` + sqlx.TypeJSONText + ` NOT NULL DEFAULT '[]',
		payload ` + sqlx.TypeJSON + ` NOT NULL DEFAULT '{}',
		amount ` + sqlx.TypeFloat + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)
`

func newConformanceDB(t *testing.T, db sqlx.DB) sqlx.DB {
	t.Helper()
	err := db.Schema(context.Background(), conformanceSchema)
	require.NoError(t, err)

	return db
}

func TestSchemaIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Store constructors run on every start, so re-applying the schema of
		// an existing database must be a no-op rather than an error.
		for range 3 {
			err := db.Schema(ctx, conformanceSchema)
			require.NoError(t, err)
		}
	})
}

func TestScalarRoundTrip(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		// Plain Go types bind on both backends: bool against SQLite INTEGER
		// and PostgreSQL BOOLEAN, string against TEXT and JSONB.
		affected, err := db.Exec(ctx, `
			INSERT INTO conformance (id, flag, doc, payload, amount, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, "a", true, `["x"]`, `{"k":1}`, 1.5, int64(1700000000))
		require.NoError(t, err)
		assert.Equal(t, int64(1), affected)

		var (
			flag      bool
			doc       []byte
			payload   []byte
			amount    float64
			updatedAt int64
		)
		err = db.QueryRow(ctx, `
			SELECT flag, doc, payload, amount, updated_at FROM conformance WHERE id = ?
		`, "a").Scan(&flag, &doc, &payload, &amount, &updatedAt)
		require.NoError(t, err)
		assert.True(t, flag)
		assert.Equal(t, `["x"]`, string(doc), "doc = %s, want [\"x\"]", doc)

		// PostgreSQL JSONB normalises whitespace, so compare parsed shape by
		// length rather than bytes; the point is that it scans into []byte.
		assert.NotEmpty(t, payload)
		assert.Equal(t, 1.5, amount)
		assert.Equal(t, int64(1700000000), updatedAt)
	})
}

func TestFalseBoolRoundTrip(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)
		_, err := db.Exec(ctx, `
			INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
		`, "f", false, int64(1))
		require.NoError(t, err)

		var flag bool
		err = db.QueryRow(ctx, `SELECT flag FROM conformance WHERE id = ?`, "f").Scan(&flag)
		require.NoError(t, err)
		assert.False(t, flag)

		// Filtering on a bool literal must work on both backends; stores use
		// this shape for `WHERE enabled = TRUE` listings.
		var count int
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance WHERE flag = ?`, false).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

func TestQueryRowNoRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		var id string
		err := db.QueryRow(ctx, `SELECT id FROM conformance WHERE id = ?`, "missing").Scan(&id)
		require.ErrorIs(t, err, sqlx.ErrNoRows)
	})
}

func TestExecReportsRowsAffected(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		for _, id := range []string{"a", "b", "c"} {
			_, err := db.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, id, true, int64(1))
			require.NoError(t, err, "seed %s: %v", id, err)
		}

		// Stores translate a zero count into "not found", so it has to be
		// exact rather than merely non-zero.
		affected, err := db.Exec(ctx, `DELETE FROM conformance WHERE id <> ?`, "a")
		require.NoError(t, err)
		assert.Equal(t, int64(2), affected)

		affected, err = db.Exec(ctx, `DELETE FROM conformance WHERE id = ?`, "missing")
		require.NoError(t, err)
		assert.Equal(t, int64(0), affected)
	})
}

func TestUpsertOnConflict(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		// The ON CONFLICT ... DO UPDATE SET ... = excluded form is shared by
		// every store; both backends accept it verbatim.
		const upsert = `
			INSERT INTO conformance (id, flag, doc, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				flag = excluded.flag,
				doc = excluded.doc,
				updated_at = excluded.updated_at
		`
		_, err := db.Exec(ctx, upsert, "a", true, `["one"]`, int64(1))
		require.NoError(t, err)
		_, err = db.Exec(ctx, upsert, "a", false, `["two"]`, int64(2))
		require.NoError(t, err)

		var (
			flag      bool
			doc       []byte
			updatedAt int64
		)
		err = db.QueryRow(ctx, `SELECT flag, doc, updated_at FROM conformance WHERE id = ?`, "a").
			Scan(&flag, &doc, &updatedAt)
		require.NoError(t, err)
		assert.False(t, flag)
		assert.Equal(t, `["two"]`, string(doc), "doc = %s, want [\"two\"]", doc)
		assert.Equal(t, int64(2), updatedAt)
	})
}

func TestQueryIteratesRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		for i, id := range []string{"a", "b", "c"} {
			_, err := db.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, id, true, int64(i))
			require.NoError(t, err, "seed %s: %v", id, err)
		}

		rows, err := db.Query(ctx, `SELECT id FROM conformance ORDER BY updated_at ASC`)
		require.NoError(t, err)

		defer rows.Close()

		var got []string
		for rows.Next() {
			var id string
			err := rows.Scan(&id)
			require.NoError(t, err)

			got = append(got, id)
		}
		err = rows.Err()
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "a", got[0])
		assert.Equal(t, "c", got[2])
	})
}

func TestQueryOnEmptyTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		rows, err := db.Query(ctx, `SELECT id FROM conformance`)
		require.NoError(t, err)

		defer rows.Close()

		assert.False(t, rows.Next())
		err = rows.Err()
		require.NoError(t, err)
	})
}

func TestInTxCommits(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		err := db.InTx(ctx, func(q sqlx.Querier) error {
			for _, id := range []string{"a", "b"} {
				if _, err := q.Exec(ctx, `
					INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
				`, id, true, int64(1)); err != nil {
					return err
				}
			}
			return nil
		})
		require.NoError(t, err)

		var count int
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestInTxRollsBackOnError(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		sentinel := errors.New("boom")
		err := db.InTx(ctx, func(q sqlx.Querier) error {
			if _, err := q.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, "a", true, int64(1)); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		// Stores rely on this: a failed multi-row replace must leave the
		// previous contents intact rather than half-applied.
		var count int
		err = db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestInTxSeesItsOwnWrites(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		err := db.InTx(ctx, func(q sqlx.Querier) error {
			if _, err := q.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, "a", true, int64(1)); err != nil {
				return err
			}
			var count int
			if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count); err != nil {
				return err
			}
			assert.Equal(t, 1, count)

			return nil
		})
		require.NoError(t, err)
	})
}

func TestRepeatedPlaceholderBindsPositionally(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)
		_, err := db.Exec(ctx, `
			INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
		`, "a", true, int64(5))
		require.NoError(t, err)

		// The preserve-on-zero update shape used by the snapshot stores binds
		// the same value twice; each `?` consumes its own argument.
		affected, err := db.Exec(ctx, `
			UPDATE conformance
			SET updated_at = CASE WHEN ? = 0 THEN updated_at ELSE ? END
			WHERE id = ?
		`, int64(0), int64(0), "a")
		require.NoError(t, err)
		assert.Equal(t, int64(1), affected)

		var updatedAt int64
		err = db.QueryRow(ctx, `SELECT updated_at FROM conformance WHERE id = ?`, "a").Scan(&updatedAt)
		require.NoError(t, err)
		assert.Equal(t, int64(5), updatedAt)
	})
}

// TestInTxIsAtomicUnderConcurrency pins what InTx actually guarantees on both
// engines: each transaction commits or rolls back as a unit.
//
// It deliberately does NOT assert that concurrent read-then-allocate sequences
// serialize. SQLite's BEGIN IMMEDIATE makes them queue, but PostgreSQL's
// default READ COMMITTED lets both read the same MAX and lets the second fail
// on the unique index instead. Callers that allocate from a MAX must cope with
// that conflict; see the note on DB.InTx.
func TestInTxIsAtomicUnderConcurrency(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		const workers = 4
		errs := make(chan error, workers)
		start := make(chan struct{})
		for worker := range workers {
			go func() {
				<-start
				errs <- db.InTx(ctx, func(q sqlx.Querier) error {
					for row := range 3 {
						id := "w" + strconv.Itoa(worker) + "-r" + strconv.Itoa(row)
						_, err := q.Exec(ctx, `
							INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
						`, id, true, int64(row))
						if err != nil {
							return err
						}
					}
					return nil
				})
			}()
		}
		close(start)
		for range workers {
			err := <-errs
			require.NoError(t, err)
		}

		var count int
		err := db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, workers*3, count)
	})
}

func TestSchemaToleratesConcurrentApplication(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		// Several gateway replicas boot against one empty database at the
		// same time, and every store constructor applies its schema on
		// start. PostgreSQL's CREATE TABLE IF NOT EXISTS is not atomic
		// across sessions: two of them racing on a missing table make the
		// loser fail with a duplicate pg_type key instead of a no-op.
		//
		// SQLite is single-instance by design (one process applies its
		// schema store by store), so the property is only pinned for
		// PostgreSQL.
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("concurrent schema application is a PostgreSQL property")
		}
		const workers = 8
		errs := make(chan error, workers)
		start := make(chan struct{})
		for range workers {
			go func() {
				<-start
				errs <- db.Schema(context.Background(), conformanceSchema,
					`CREATE INDEX IF NOT EXISTS conformance_updated_at ON conformance (updated_at)`)
			}()
		}
		close(start)
		for range workers {
			err := <-errs
			assert.NoError(t, err)
		}
	})
}
