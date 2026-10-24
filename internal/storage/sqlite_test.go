package storage

import (
	"context"
	"fmt"
	"path/filepath"
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
