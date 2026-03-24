package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSQLiteConcurrentWriteSafety(t *testing.T) {
	store, err := NewSQLite(SQLiteConfig{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("failed to create SQLite storage: %v", err)
	}
	defer store.Close()

	db := store.SQLiteDB()

	// Create two tables to simulate audit log and usage tracking writing concurrently.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_audit (id TEXT PRIMARY KEY, data TEXT)`)
	if err != nil {
		t.Fatalf("failed to create test_audit table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_usage (id TEXT PRIMARY KEY, data TEXT)`)
	if err != nil {
		t.Fatalf("failed to create test_usage table: %v", err)
	}

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
	if err := db.QueryRow("SELECT COUNT(*) FROM test_audit").Scan(&auditCount); err != nil {
		t.Fatalf("failed to count audit rows: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM test_usage").Scan(&usageCount); err != nil {
		t.Fatalf("failed to count usage rows: %v", err)
	}

	expectedPerTable := (goroutines / 2) * insertsPerGoroutine
	if auditCount != expectedPerTable {
		t.Errorf("test_audit: got %d rows, want %d", auditCount, expectedPerTable)
	}
	if usageCount != expectedPerTable {
		t.Errorf("test_usage: got %d rows, want %d", usageCount, expectedPerTable)
	}
}
