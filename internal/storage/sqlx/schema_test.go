package sqlx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddColumnStatement(t *testing.T) {
	tests := []struct {
		name      string
		dialect   Dialect
		statement string
		want      string
	}{
		{
			name:      "postgresql adds IF NOT EXISTS",
			dialect:   PostgreSQL,
			statement: `ALTER TABLE t ADD COLUMN flag ` + TypeBool + ` NOT NULL DEFAULT FALSE`,
			want:      `ALTER TABLE t ADD COLUMN IF NOT EXISTS flag BOOLEAN NOT NULL DEFAULT FALSE`,
		},
		{
			name:      "postgresql keeps an explicit IF NOT EXISTS",
			dialect:   PostgreSQL,
			statement: `ALTER TABLE t ADD COLUMN IF NOT EXISTS note TEXT`,
			want:      `ALTER TABLE t ADD COLUMN IF NOT EXISTS note TEXT`,
		},
		{
			name:      "sqlite has no IF NOT EXISTS for columns",
			dialect:   SQLite,
			statement: `ALTER TABLE t ADD COLUMN flag ` + TypeBool + ` NOT NULL DEFAULT FALSE`,
			want:      `ALTER TABLE t ADD COLUMN flag INTEGER NOT NULL DEFAULT FALSE`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.dialect.addColumnStatement(tt.statement))
		})
	}
}

func TestParseAddColumn(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		table     string
		column    string
		ok        bool
	}{
		{"plain", `ALTER TABLE usage ADD COLUMN user_path TEXT`, "usage", "user_path", true},
		{"type token", `ALTER TABLE t ADD COLUMN flag ` + TypeBool + ` NOT NULL DEFAULT FALSE`, "t", "flag", true},
		{"if not exists", `ALTER TABLE t ADD COLUMN IF NOT EXISTS note TEXT`, "t", "note", true},
		{"mixed case and whitespace", "alter  table Audit_Logs\n\tadd column Session_ID TEXT", "audit_logs", "session_id", true},
		{"rename is not an add", `ALTER TABLE t RENAME TO t_old`, "", "", false},
		{"drop column", `ALTER TABLE t DROP COLUMN note`, "", "", false},
		{"quoted column", `ALTER TABLE t ADD COLUMN "Note" TEXT`, "", "", false},
		{"schema-qualified table", `ALTER TABLE s.t ADD COLUMN note TEXT`, "", "", false},
		{"too short", `ALTER TABLE t ADD COLUMN`, "", "", false},
		{"several actions", `ALTER TABLE t ADD COLUMN a TEXT, ADD COLUMN b TEXT`, "", "", false},
		{"comma in a type", `ALTER TABLE t ADD COLUMN amount NUMERIC(10,2)`, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table, column, ok := parseAddColumn(tt.statement)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.table, table)
			assert.Equal(t, tt.column, column)
		})
	}
}
