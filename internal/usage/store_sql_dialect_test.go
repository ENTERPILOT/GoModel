package usage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// dialectDB reports a dialect of the test's choosing, standing in for an
// engine the default build cannot open.
type dialectDB struct {
	sqlx.DB
	dialect sqlx.Dialect
}

func (d dialectDB) Dialect() sqlx.Dialect { return d.dialect }

func TestCheckDialect(t *testing.T) {
	tests := []struct {
		dialect sqlx.Dialect
		wantErr bool
	}{
		{dialect: sqlx.SQLite},
		{dialect: sqlx.PostgreSQL},
		{dialect: sqlx.DuckDB, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.dialect), func(t *testing.T) {
			err := checkDialect(tt.dialect)
			if tt.wantErr {
				require.ErrorContains(t, err, string(tt.dialect))
				return
			}
			require.NoError(t, err)
		})
	}
}

// The store and the reader refuse DuckDB before a usage table exists, so a
// later DuckDB dialect is free to choose its own schema.
func TestSQLStoreRejectsDuckDB(t *testing.T) {
	ctx := context.Background()
	sqlite := sqlxtest.NewSQLite(t)
	db := dialectDB{DB: sqlite, dialect: sqlx.DuckDB}

	_, err := NewSQLStore(ctx, db, 0)
	require.ErrorContains(t, err, "duckdb")
	_, err = NewSQLReader(db)
	require.ErrorContains(t, err, "duckdb")

	var tables int
	require.NoError(t, sqlite.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'usage'").Scan(&tables))
	assert.Zero(t, tables, "no usage table is created")
}
