package auditlog

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// TestSQLReader_LastUsedUsesAuthKeyTimestampIndex reads the query plan to
// prove the last-used lookup is served by the auth_key_id + timestamp index.
func TestSQLReader_LastUsedUsesAuthKeyTimestampIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		defer store.Close()
		store.indexBuild.Wait()

		var plan []string
		err = db.InTx(ctx, func(q sqlx.Querier) error {
			explain := "EXPLAIN QUERY PLAN "
			if db.Dialect() == sqlx.PostgreSQL {
				// A sequential scan is always cheaper on an empty table.
				if _, err := q.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
					return err
				}
				explain = "EXPLAIN "
			}
			rows, err := q.Query(ctx, explain+lastUsedQuery(2), "key-a", "key-b")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if db.Dialect() == sqlx.PostgreSQL {
					var line string
					if err := rows.Scan(&line); err != nil {
						return err
					}
					plan = append(plan, line)
					continue
				}
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					return err
				}
				plan = append(plan, detail)
			}
			return rows.Err()
		})
		require.NoError(t, err)
		assert.Contains(t, strings.Join(plan, "\n"), "idx_audit_auth_key_timestamp")
	})
}

// TestSQLStore_ReplacesLegacyAuthKeyIndex upgrades a database that still has
// the single-column auth_key_id index: the composite replaces it.
func TestSQLStore_ReplacesLegacyAuthKeyIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		first, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		first.indexBuild.Wait()
		require.NoError(t, first.Close())
		_, err = db.Exec(ctx, "DROP INDEX IF EXISTS "+authKeyTimestampIndex)
		require.NoError(t, err)
		_, err = db.Exec(ctx, "CREATE INDEX "+legacyAuthKeySQLIndex+" ON audit_logs(auth_key_id)")
		require.NoError(t, err)

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		defer store.Close()
		store.indexBuild.Wait()

		assert.True(t, sqlIndexExists(ctx, t, db, authKeyTimestampIndex))
		assert.False(t, sqlIndexExists(ctx, t, db, legacyAuthKeySQLIndex))
	})
}

func sqlIndexExists(ctx context.Context, t *testing.T, db sqlx.DB, name string) bool {
	t.Helper()
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if db.Dialect() == sqlx.PostgreSQL {
		query = `SELECT COUNT(*) FROM pg_index WHERE indexrelid = to_regclass(?) AND indisvalid`
	}
	var count int
	require.NoError(t, db.QueryRow(ctx, query, name).Scan(&count))
	return count > 0
}
