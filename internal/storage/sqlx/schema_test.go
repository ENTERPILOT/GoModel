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
		{
			name:      "duckdb drops NOT NULL it cannot add",
			dialect:   DuckDB,
			statement: `ALTER TABLE t ADD COLUMN flag ` + TypeBool + ` NOT NULL DEFAULT FALSE`,
			want:      `ALTER TABLE t ADD COLUMN flag BOOLEAN DEFAULT FALSE`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.dialect.addColumnStatement(tt.statement))
		})
	}
}
