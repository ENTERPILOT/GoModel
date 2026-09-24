package auditlog

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestEnsureAuthKeyIndexLeavesAnotherInstancesBuild starts a concurrent build
// that stays in progress (an open writer holds it), then checks a second
// instance's startup neither drops that index nor blocks on it.
func TestEnsureAuthKeyIndexLeavesAnotherInstancesBuild(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("concurrent index builds are PostgreSQL only")
		}
		ctx := context.Background()
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		store.indexBuild.Wait()
		require.NoError(t, store.Close())
		_, err = db.Exec(ctx, "DROP INDEX "+authKeyTimestampIndex)
		require.NoError(t, err)

		// Release the held writer on every exit path, or a failed assertion
		// leaves the build and the schema cleanup waiting on it forever.
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseWriter)
		writerDone := make(chan error, 1)
		writerStarted := make(chan struct{})
		go func() {
			writerDone <- db.InTx(ctx, func(q sqlx.Querier) error {
				if _, err := q.Exec(ctx, "INSERT INTO audit_logs (id, timestamp) VALUES ('held', NOW())"); err != nil {
					close(writerStarted)
					return err
				}
				close(writerStarted)
				<-release
				return nil
			})
		}()
		<-writerStarted
		buildDone := make(chan error, 1)
		go func() {
			_, err := db.Exec(ctx, "CREATE INDEX CONCURRENTLY "+authKeyTimestampIndex+" ON audit_logs(auth_key_id, timestamp)")
			buildDone <- err
		}()
		require.Eventually(t, func() bool { return indexBuildInProgress(ctx, db, authKeyTimestampIndex) }, 10*time.Second, 20*time.Millisecond)

		finished := make(chan struct{})
		go func() {
			ensureAuthKeyTimestampIndex(ctx, db)
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("startup blocked on another instance's index build")
		}
		_, exists := indexValidity(ctx, db, authKeyTimestampIndex)
		assert.True(t, exists, "the in-progress index was dropped")

		releaseWriter()
		require.NoError(t, <-writerDone)
		require.NoError(t, <-buildDone)
		valid, _ := indexValidity(ctx, db, authKeyTimestampIndex)
		assert.True(t, valid)
	})
}
