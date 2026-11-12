package storage

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLitePing(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "ping.db")})
	require.NoError(t, err)

	hc, ok := store.(HealthChecker)
	require.True(t, ok)
	err = hc.Ping(context.Background())
	require.NoError(t, err)
	err = store.Close()
	require.NoError(t, err)
	require.Error(t, hc.Ping(context.Background()))
}

func fileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}

// The database holds provider credentials and request content, so what
// NewSQLite creates is private to its owner, WAL and shared-memory files
// included.
func TestSQLiteCreatesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "nested", "data")
	path := filepath.Join(dir, "gomodel.db")

	store, err := NewSQLite(SQLiteConfig{Path: path})
	require.NoError(t, err)
	defer store.Close()
	_, err = store.DB().Exec(`CREATE TABLE test_private (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	assert.Equal(t, fs.FileMode(0o700), fileMode(t, dir))
	assert.Equal(t, fs.FileMode(0o700), fileMode(t, filepath.Dir(dir)))
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		assert.Equal(t, fs.FileMode(0o600), fileMode(t, name), name)
	}
}

// An operator may share an existing directory or database with another user
// on purpose, so their modes are left alone and a readable database is only
// reported.
func TestSQLiteKeepsExistingModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits")
	}
	tests := []struct {
		name        string
		mode        fs.FileMode
		wantWarning bool
	}{
		{name: "world readable", mode: 0o644, wantWarning: true},
		{name: "group readable", mode: 0o640, wantWarning: true},
		{name: "private", mode: 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })

			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o755))
			path := filepath.Join(dir, "gomodel.db")
			require.NoError(t, os.WriteFile(path, nil, tt.mode))
			require.NoError(t, os.Chmod(path, tt.mode))

			store, err := NewSQLite(SQLiteConfig{Path: path})
			require.NoError(t, err)
			defer store.Close()
			_, err = store.DB().Exec(`CREATE TABLE test_existing (id TEXT PRIMARY KEY)`)
			require.NoError(t, err)

			assert.Equal(t, fs.FileMode(0o755), fileMode(t, dir))
			assert.Equal(t, tt.mode, fileMode(t, path))
			if tt.wantWarning {
				assert.Contains(t, logs.String(), "accessible to other users")
				assert.Contains(t, logs.String(), path)
			} else {
				assert.NotContains(t, logs.String(), "accessible to other users")
			}
		})
	}
}

func TestSQLiteConcurrentWriteSafety(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "test.db")})
	require.NoError(t, err)

	defer store.Close()

	db := store.DB()

	// Create two tables to simulate audit log and usage tracking writing concurrently.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_audit (id TEXT PRIMARY KEY, data TEXT)`)
	require.NoError(t, err)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_usage (id TEXT PRIMARY KEY, data TEXT)`)
	require.NoError(t, err)

	const goroutines = 10
	const insertsPerGoroutine = 50

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*insertsPerGoroutine*2)

	// Half the goroutines write to test_audit, half to test_usage — mirrors real workload.
	for i := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			table := "test_audit"
			if id%2 == 1 {
				table = "test_usage"
			}
			for j := range insertsPerGoroutine {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (id, data) VALUES (?, ?)`, table),
					fmt.Sprintf("%d-%d", id, j), "payload")
				cancel()
				if err != nil {
					errs <- fmt.Errorf("goroutine %d insert %d into %s: %w", id, j, table, err)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}

	// Verify all rows were inserted.
	var auditCount, usageCount int
	err = db.QueryRow("SELECT COUNT(*) FROM test_audit").Scan(&auditCount)
	require.NoError(t, err)
	err = db.QueryRow("SELECT COUNT(*) FROM test_usage").Scan(&usageCount)
	require.NoError(t, err)

	expectedPerTable := (goroutines / 2) * insertsPerGoroutine
	assert.Equal(t, expectedPerTable, auditCount)
	assert.Equal(t, expectedPerTable, usageCount)
}

func TestSQLiteTuningPragmas(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "pragmas.db")})
	require.NoError(t, err)
	defer store.Close()

	tests := []struct {
		pragma string
		want   string
	}{
		{pragma: "journal_mode", want: "wal"},
		{pragma: "synchronous", want: "1"},
		{pragma: "busy_timeout", want: "5000"},
		{pragma: "cache_size", want: "-65536"},
		{pragma: "mmap_size", want: "268435456"},
		{pragma: "journal_size_limit", want: "67108864"},
		{pragma: "analysis_limit", want: "100000"},
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			require.NoError(t, store.DB().QueryRow("PRAGMA "+tt.pragma).Scan(&got))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSQLiteMaintenanceAnalyzesTables(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "analyze.db")})
	require.NoError(t, err)
	db := store.DB()

	_, err = db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, model TEXT)`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE INDEX idx_items_model ON items(model)`)
	require.NoError(t, err)
	_, err = db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100)
		INSERT INTO items (model) SELECT 'model-' || (i % 5) FROM n`)
	require.NoError(t, err)

	StartMaintenance(store)
	StartMaintenance(store) // only the first call does anything

	// The first check completes before StartMaintenance returns.
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_stat1 WHERE tbl = 'items'`).Scan(&rows))
	assert.Positive(t, rows)

	require.NoError(t, store.Close())
}

func TestSQLiteMaintenanceAfterClose(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "closed.db")})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	// Returns at once without touching the closed database or panicking.
	assert.NotPanics(t, func() { StartMaintenance(store) })
}
