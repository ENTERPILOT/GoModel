package auditlog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// BenchmarkSQLStoreWriteBatch measures one audit flush against a file-backed
// SQLite database opened the way the gateway opens it, and against PostgreSQL
// when GOMODEL_TEST_POSTGRES_URL is set. Both audit tables are dropped first
// there, so point it at a throwaway database.
func BenchmarkSQLStoreWriteBatch(b *testing.B) {
	// The background index builds log as they finish; keep the output parsable.
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.DiscardHandler))

	for _, size := range []int{BatchFlushThreshold, 1000} {
		b.Run(fmt.Sprintf("sqlite/%d", size), func(b *testing.B) {
			sqlite, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(b.TempDir(), "audit.db")})
			if err != nil {
				b.Fatal(err)
			}
			defer sqlite.Close()
			db, err := sqlx.NewSQLite(sqlite.DB())
			if err != nil {
				b.Fatal(err)
			}
			benchmarkWriteBatch(b, db, size)
		})
		b.Run(fmt.Sprintf("postgresql/%d", size), func(b *testing.B) {
			url := strings.TrimSpace(os.Getenv(sqlxtest.PostgresURLEnv))
			if url == "" {
				b.Skipf("%s not set", sqlxtest.PostgresURLEnv)
			}
			pool, err := pgxpool.New(context.Background(), url)
			if err != nil {
				b.Fatal(err)
			}
			defer pool.Close()
			db, err := sqlx.NewPostgreSQL(pool)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := db.Exec(context.Background(), `DROP TABLE IF EXISTS audit_log_attempts, audit_logs`); err != nil {
				b.Fatal(err)
			}
			benchmarkWriteBatch(b, db, size)
		})
	}
}

func benchmarkWriteBatch(b *testing.B, db sqlx.DB, size int) {
	ctx := context.Background()
	store, err := NewSQLStore(ctx, db, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	store.indexBuild.Wait()

	now := time.Unix(1700000000, 0).UTC()
	entries := make([]*LogEntry, size)
	for i := range entries {
		entries[i] = testLogEntry("", now)
		if i%10 == 0 {
			entries[i].Data.Attempts = []AttemptSnapshot{{Seq: 1, Kind: AttemptKindFailover, ProviderType: "openai", StatusCode: 503}}
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for n := range b.N {
		b.StopTimer()
		for i, entry := range entries {
			entry.ID = fmt.Sprintf("bench-%d-%d", n, i)
		}
		b.StartTimer()
		if err := store.WriteBatch(ctx, entries); err != nil {
			b.Fatal(err)
		}
	}
}
