package sqlx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
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
	if err := db.Schema(context.Background(), conformanceSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func TestSchemaIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Store constructors run on every start, so re-applying the schema of
		// an existing database must be a no-op rather than an error.
		for range 3 {
			if err := db.Schema(ctx, conformanceSchema); err != nil {
				t.Fatalf("re-apply schema: %v", err)
			}
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
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if affected != 1 {
			t.Errorf("insert affected = %d, want 1", affected)
		}

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
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if !flag {
			t.Error("flag = false, want true")
		}
		if string(doc) != `["x"]` {
			t.Errorf("doc = %s, want [\"x\"]", doc)
		}
		// PostgreSQL JSONB normalises whitespace, so compare parsed shape by
		// length rather than bytes; the point is that it scans into []byte.
		if len(payload) == 0 {
			t.Error("payload scanned empty")
		}
		if amount != 1.5 {
			t.Errorf("amount = %v, want 1.5", amount)
		}
		if updatedAt != 1700000000 {
			t.Errorf("updated_at = %d, want 1700000000", updatedAt)
		}
	})
}

func TestFalseBoolRoundTrip(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		if _, err := db.Exec(ctx, `
			INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
		`, "f", false, int64(1)); err != nil {
			t.Fatalf("insert: %v", err)
		}

		var flag bool
		if err := db.QueryRow(ctx, `SELECT flag FROM conformance WHERE id = ?`, "f").Scan(&flag); err != nil {
			t.Fatalf("select: %v", err)
		}
		if flag {
			t.Error("flag = true, want false")
		}

		// Filtering on a bool literal must work on both backends; stores use
		// this shape for `WHERE enabled = TRUE` listings.
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance WHERE flag = ?`, false).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Errorf("count = %d, want 1", count)
		}
	})
}

func TestQueryRowNoRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		var id string
		err := db.QueryRow(ctx, `SELECT id FROM conformance WHERE id = ?`, "missing").Scan(&id)
		if !errors.Is(err, sqlx.ErrNoRows) {
			t.Fatalf("Scan error = %v, want sqlx.ErrNoRows", err)
		}
	})
}

func TestExecReportsRowsAffected(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		for _, id := range []string{"a", "b", "c"} {
			if _, err := db.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, id, true, int64(1)); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}

		// Stores translate a zero count into "not found", so it has to be
		// exact rather than merely non-zero.
		affected, err := db.Exec(ctx, `DELETE FROM conformance WHERE id <> ?`, "a")
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if affected != 2 {
			t.Errorf("delete affected = %d, want 2", affected)
		}

		affected, err = db.Exec(ctx, `DELETE FROM conformance WHERE id = ?`, "missing")
		if err != nil {
			t.Fatalf("delete missing: %v", err)
		}
		if affected != 0 {
			t.Errorf("delete missing affected = %d, want 0", affected)
		}
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
		if _, err := db.Exec(ctx, upsert, "a", true, `["one"]`, int64(1)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := db.Exec(ctx, upsert, "a", false, `["two"]`, int64(2)); err != nil {
			t.Fatalf("update: %v", err)
		}

		var (
			flag      bool
			doc       []byte
			updatedAt int64
		)
		err := db.QueryRow(ctx, `SELECT flag, doc, updated_at FROM conformance WHERE id = ?`, "a").
			Scan(&flag, &doc, &updatedAt)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if flag {
			t.Error("flag was not overwritten")
		}
		if string(doc) != `["two"]` {
			t.Errorf("doc = %s, want [\"two\"]", doc)
		}
		if updatedAt != 2 {
			t.Errorf("updated_at = %d, want 2", updatedAt)
		}
	})
}

func TestQueryIteratesRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		for i, id := range []string{"a", "b", "c"} {
			if _, err := db.Exec(ctx, `
				INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
			`, id, true, int64(i)); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}

		rows, err := db.Query(ctx, `SELECT id FROM conformance ORDER BY updated_at ASC`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()

		var got []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows.Err: %v", err)
		}
		if len(got) != 3 || got[0] != "a" || got[2] != "c" {
			t.Errorf("ids = %v, want [a b c]", got)
		}
	})
}

func TestQueryOnEmptyTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		rows, err := db.Query(ctx, `SELECT id FROM conformance`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()

		if rows.Next() {
			t.Error("Next() = true on an empty table")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows.Err: %v", err)
		}
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
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}

		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2", count)
		}
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
		if !errors.Is(err, sentinel) {
			t.Fatalf("InTx error = %v, want sentinel", err)
		}

		// Stores rely on this: a failed multi-row replace must leave the
		// previous contents intact rather than half-applied.
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM conformance`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 0 {
			t.Errorf("count = %d after rollback, want 0", count)
		}
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
			if count != 1 {
				t.Errorf("in-transaction count = %d, want 1", count)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}
	})
}

func TestRepeatedPlaceholderBindsPositionally(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		newConformanceDB(t, db)

		if _, err := db.Exec(ctx, `
			INSERT INTO conformance (id, flag, updated_at) VALUES (?, ?, ?)
		`, "a", true, int64(5)); err != nil {
			t.Fatalf("insert: %v", err)
		}

		// The preserve-on-zero update shape used by the snapshot stores binds
		// the same value twice; each `?` consumes its own argument.
		affected, err := db.Exec(ctx, `
			UPDATE conformance
			SET updated_at = CASE WHEN ? = 0 THEN updated_at ELSE ? END
			WHERE id = ?
		`, int64(0), int64(0), "a")
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if affected != 1 {
			t.Errorf("affected = %d, want 1", affected)
		}

		var updatedAt int64
		if err := db.QueryRow(ctx, `SELECT updated_at FROM conformance WHERE id = ?`, "a").Scan(&updatedAt); err != nil {
			t.Fatalf("select: %v", err)
		}
		if updatedAt != 5 {
			t.Errorf("updated_at = %d, want 5 preserved", updatedAt)
		}
	})
}
