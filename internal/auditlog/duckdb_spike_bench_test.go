//go:build duckdb

package auditlog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// TestDuckDBSpikeBenchmark compares file-backed SQLite (production settings)
// and DuckDB on the audit log write and read paths. It is opt-in:
//
//	GOMODEL_DUCKDB_BENCH_ROWS=300000 go test -tags duckdb -run DuckDBSpike -v ./internal/auditlog/
func TestDuckDBSpikeBenchmark(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("GOMODEL_DUCKDB_BENCH_ROWS"))
	if rows <= 0 {
		t.Skip("GOMODEL_DUCKDB_BENCH_ROWS not set")
	}
	dir := t.TempDir()

	sqliteStore, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(dir, "bench.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqliteStore.Close() })
	sqliteDB, err := sqlx.NewSQLite(sqliteStore.DB())
	require.NoError(t, err)

	duckRaw, err := sql.Open("duckdb", filepath.Join(dir, "bench.duckdb"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = duckRaw.Close() })
	duckDB, err := sqlx.NewDuckDB(duckRaw)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		db   sqlx.DB
		file string
	}{
		{"sqlite", sqliteDB, filepath.Join(dir, "bench.db")},
		{"duckdb", duckDB, filepath.Join(dir, "bench.duckdb")},
	} {
		runSpikeBenchmark(t, tc.name, tc.db, tc.file, rows)
	}
}

func runSpikeBenchmark(t *testing.T, name string, db sqlx.DB, file string, rows int) {
	ctx := context.Background()
	store, err := NewSQLStore(ctx, db, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	reader, err := NewSQLReader(db)
	require.NoError(t, err)

	models := []string{"gpt-4o-mini", "gpt-4o", "claude-sonnet", "gemini-flash", "llama-3"}
	providers := []string{"openai", "anthropic", "gemini", "groq"}
	end := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -30)
	step := end.Sub(start) / time.Duration(rows)

	const batch = 500
	began := time.Now()
	for offset := 0; offset < rows; offset += batch {
		entries := make([]*LogEntry, 0, batch)
		for i := offset; i < min(offset+batch, rows); i++ {
			status := 200
			if i%50 == 0 {
				status = 500
			}
			content := fmt.Sprintf("request %d about the weather in city %d", i, i%997)
			if i%10007 == 0 {
				content += " needle"
			}
			entries = append(entries, &LogEntry{
				ID:             uuid.NewString(),
				Timestamp:      start.Add(time.Duration(i) * step),
				DurationNs:     int64(100_000_000 + i%900_000_000),
				RequestedModel: models[i%len(models)],
				Provider:       providers[i%len(providers)],
				ProviderName:   providers[i%len(providers)],
				StatusCode:     status,
				RequestID:      uuid.NewString(),
				Method:         "POST",
				Path:           "/v1/chat/completions",
				UserPath:       fmt.Sprintf("/team-%d/user-%d", i%20, i%400),
				Data: &LogData{
					UserAgent:    "bench/1.0",
					RequestBody:  map[string]any{"model": models[i%len(models)], "messages": []any{map[string]any{"role": "user", "content": content}}},
					ResponseBody: map[string]any{"id": "chatcmpl-" + strconv.Itoa(i), "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "It is sunny with a light breeze and a high of twenty-two degrees."}}}},
				},
			})
		}
		require.NoError(t, store.WriteBatch(ctx, entries))
	}
	ingest := time.Since(began)

	time30d := QueryParams{StartDate: start, EndDate: end}
	timed := func(label string, fn func() error) {
		// Warm once, then report the best of three.
		require.NoError(t, fn(), label)
		best := time.Duration(1<<63 - 1)
		for range 3 {
			began := time.Now()
			require.NoError(t, fn(), label)
			best = min(best, time.Since(began))
		}
		t.Logf("%-7s %-34s %10s", name, label, best.Round(100*time.Microsecond))
	}

	t.Logf("%-7s %-34s %10s (%.0f rows/s)", name, "ingest "+strconv.Itoa(rows)+" rows, batches of 500", ingest.Round(time.Millisecond), float64(rows)/ingest.Seconds())
	timed("stats 30d hourly", func() error {
		_, err := reader.GetRequestStats(ctx, RequestStatsParams{QueryParams: time30d, Interval: StatsIntervalHour, Location: time.UTC})
		return err
	})
	timed("stats 30d daily, user subtree", func() error {
		_, err := reader.GetRequestStats(ctx, RequestStatsParams{QueryParams: time30d, Interval: StatsIntervalDay, Location: time.UTC, UserPath: "/team-3"})
		return err
	})
	timed("list page 1 (50) + total", func() error {
		_, err := reader.GetLogs(ctx, LogQueryParams{QueryParams: time30d, Limit: 50})
		return err
	})
	timed("list filtered by model", func() error {
		_, err := reader.GetLogs(ctx, LogQueryParams{QueryParams: time30d, RequestedModel: "claude-sonnet", Limit: 50})
		return err
	})
	timed("list deep page (offset 10000)", func() error {
		_, err := reader.GetLogs(ctx, LogQueryParams{QueryParams: time30d, Limit: 50, Offset: 10000})
		return err
	})
	timed("free-text search", func() error {
		_, err := reader.GetLogs(ctx, LogQueryParams{QueryParams: time30d, Search: "needle", Limit: 50})
		return err
	})
	timed("get by id", func() error {
		_, err := reader.GetLogByID(ctx, uuid.NewString())
		return err
	})

	// Make the DuckDB WAL land in the main file so the size is comparable.
	if db.Dialect() == sqlx.DuckDB {
		_, err := db.Exec(ctx, "CHECKPOINT")
		require.NoError(t, err)
	} else {
		_, err := db.Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		require.NoError(t, err)
	}
	info, err := os.Stat(file)
	require.NoError(t, err)
	t.Logf("%-7s %-34s %8.1f MB", name, "file size", float64(info.Size())/1e6)
}
