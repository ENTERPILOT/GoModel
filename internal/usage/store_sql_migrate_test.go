package usage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// alterRecorder records the ALTER TABLE statements a store runs.
type alterRecorder struct {
	sqlx.DB
	alters []string
}

func (r *alterRecorder) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	if strings.HasPrefix(strings.TrimSpace(query), "ALTER TABLE") {
		r.alters = append(r.alters, query)
	}
	return r.DB.Exec(ctx, query, args...)
}

// Every start re-runs the column migrations. On PostgreSQL even a no-op
// ADD COLUMN IF NOT EXISTS takes an exclusive lock on the table, so a restart
// against an up-to-date schema must issue no ALTER at all.
func TestEnsureUsageSchemaSkipsAppliedColumns(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		require.NoError(t, ensureUsageSchema(ctx, db))

		restart := &alterRecorder{DB: db}
		require.NoError(t, ensureUsageSchema(ctx, restart))
		assert.Empty(t, restart.alters, "restart against an up-to-date schema")

		_, err := db.Exec(ctx, "ALTER TABLE usage DROP COLUMN costs_calculation_caveat")
		require.NoError(t, err)
		upgrade := &alterRecorder{DB: db}
		require.NoError(t, ensureUsageSchema(ctx, upgrade))
		require.Len(t, upgrade.alters, 1)
		assert.Contains(t, upgrade.alters[0], "costs_calculation_caveat")

		columns, err := sqlx.Columns(ctx, db, "usage")
		require.NoError(t, err)
		assert.True(t, columns["costs_calculation_caveat"], "missing column is added back")
	})
}
