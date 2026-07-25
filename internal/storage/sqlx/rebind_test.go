package sqlx

import (
	"strings"
	"testing"
)

// A miscounted placeholder silently shifts every argument after it, which no
// integration test reliably catches — so the scanner is pinned directly.
func TestRebind(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "no placeholders is unchanged",
			query: `SELECT id FROM t`,
			want:  `SELECT id FROM t`,
		},
		{
			name:  "numbers in order",
			query: `INSERT INTO t (a, b, c) VALUES (?, ?, ?)`,
			want:  `INSERT INTO t (a, b, c) VALUES ($1, $2, $3)`,
		},
		{
			name:  "repeated value gets its own number",
			query: `SET x = CASE WHEN ? = 0 THEN x ELSE ? END`,
			want:  `SET x = CASE WHEN $1 = 0 THEN x ELSE $2 END`,
		},
		{
			name:  "question mark inside a string literal is not a placeholder",
			query: `SELECT ? WHERE label = 'why?' AND id = ?`,
			want:  `SELECT $1 WHERE label = 'why?' AND id = $2`,
		},
		{
			name:  "doubled quote escapes inside a literal",
			query: `SELECT ? WHERE label = 'it''s a ? really' AND id = ?`,
			want:  `SELECT $1 WHERE label = 'it''s a ? really' AND id = $2`,
		},
		{
			name:  "question mark inside a quoted identifier",
			query: `SELECT "odd?column" FROM t WHERE id = ?`,
			want:  `SELECT "odd?column" FROM t WHERE id = $1`,
		},
		{
			name:  "question mark inside a line comment",
			query: "SELECT id -- why? not\nFROM t WHERE id = ?",
			want:  "SELECT id -- why? not\nFROM t WHERE id = $1",
		},
		{
			name:  "line comment running to end of input",
			query: `SELECT id FROM t -- trailing? comment`,
			want:  `SELECT id FROM t -- trailing? comment`,
		},
		{
			name:  "question mark inside a block comment",
			query: `SELECT id /* is this ? a placeholder */ FROM t WHERE id = ?`,
			want:  `SELECT id /* is this ? a placeholder */ FROM t WHERE id = $1`,
		},
		{
			name:  "dollar quoted body is left intact",
			query: `DO $$ BEGIN RAISE NOTICE 'what?'; END $$; SELECT ?`,
			want:  `DO $$ BEGIN RAISE NOTICE 'what?'; END $$; SELECT $1`,
		},
		{
			name:  "tagged dollar quote is left intact",
			query: `DO $body$ SELECT '?' $body$; SELECT ?`,
			want:  `DO $body$ SELECT '?' $body$; SELECT $1`,
		},
		{
			name:  "existing positional parameter is not mistaken for a dollar quote",
			query: `SELECT $1, ?`,
			want:  `SELECT $1, $1`,
		},
		{
			name:  "unterminated literal does not lose the tail",
			query: `SELECT ? WHERE x = 'oops`,
			want:  `SELECT $1 WHERE x = 'oops`,
		},
		{
			name:  "unterminated block comment does not lose the tail",
			query: `SELECT ? /* oops`,
			want:  `SELECT $1 /* oops`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebind(tt.query); got != tt.want {
				t.Errorf("rebind()\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestExpandTypes(t *testing.T) {
	const ddl = `CREATE TABLE t (
		id TEXT PRIMARY KEY,
		n ` + TypeInt64 + ` NOT NULL,
		flag ` + TypeBool + ` NOT NULL DEFAULT 1,
		cost ` + TypeFloat + `,
		doc ` + TypeJSONText + ` NOT NULL DEFAULT '[]',
		blob ` + TypeJSON + `,
		at ` + TypeTimestamp + `
	)`

	tests := []struct {
		dialect Dialect
		want    []string
	}{
		{SQLite, []string{"INTEGER NOT NULL", "INTEGER NOT NULL DEFAULT 1", "REAL", "TEXT NOT NULL", "JSON", "DATETIME"}},
		{PostgreSQL, []string{"BIGINT NOT NULL", "BOOLEAN NOT NULL DEFAULT 1", "DOUBLE PRECISION", "JSONB NOT NULL", "JSONB", "TIMESTAMPTZ"}},
	}

	for _, tt := range tests {
		t.Run(string(tt.dialect), func(t *testing.T) {
			got := tt.dialect.ExpandTypes(ddl)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("expansion missing %q:\n%s", want, got)
				}
			}
			// An unexpanded token would reach the database as invalid SQL.
			if strings.Contains(got, "{") {
				t.Errorf("expansion left a token behind:\n%s", got)
			}
		})
	}
}

// TestExpandTypesLeavesUnrelatedBracesAlone guards the JSON defaults that
// legitimately contain braces, such as DEFAULT '{}'.
func TestExpandTypesLeavesUnrelatedBracesAlone(t *testing.T) {
	const ddl = `headers ` + TypeJSONText + ` NOT NULL DEFAULT '{}'`
	for _, dialect := range []Dialect{SQLite, PostgreSQL} {
		got := dialect.ExpandTypes(ddl)
		if !strings.Contains(got, `DEFAULT '{}'`) {
			t.Errorf("%s: default was rewritten: %s", dialect, got)
		}
	}
}
