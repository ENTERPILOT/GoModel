package auditlog

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// BenchmarkSQLStoreWriteBatchAttempts writes one batch of 100 entries with
// three provider attempts each into an in-memory SQLite store that already
// holds 30,000 attempts, so every insert maintains B-trees of realistic depth.
// Memory keeps disk latency out of the comparison of index maintenance cost.
func BenchmarkSQLStoreWriteBatchAttempts(b *testing.B) {
	const (
		entriesPerBatch  = 100
		attemptsPerEntry = 3
		seedBatches      = 100
	)
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:bench-attempts?mode=memory&cache=shared")
	if err != nil {
		b.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	db, err := sqlx.NewSQLite(raw)
	if err != nil {
		b.Fatal(err)
	}
	store, err := NewSQLStore(ctx, db, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0).UTC()
	batch := func(n int) []*LogEntry {
		entries := make([]*LogEntry, entriesPerBatch)
		for i := range entries {
			entry := testLogEntry(fmt.Sprintf("bench-%07d-%03d", n, i), now)
			if entry.Data == nil {
				entry.Data = &LogData{}
			}
			for seq := 1; seq <= attemptsPerEntry; seq++ {
				entry.Data.Attempts = append(entry.Data.Attempts, AttemptSnapshot{
					Seq:          seq,
					Kind:         AttemptKindFailover,
					ProviderType: "openai",
					Model:        "openai/gpt-5.5",
					StatusCode:   503,
				})
			}
			entries[i] = entry
		}
		return entries
	}
	for n := range seedBatches {
		if err := store.WriteBatch(ctx, batch(n)); err != nil {
			b.Fatal(err)
		}
	}

	batches := make([][]*LogEntry, b.N)
	for n := range batches {
		batches[n] = batch(seedBatches + n)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := range b.N {
		if err := store.WriteBatch(ctx, batches[n]); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*entriesPerBatch*attemptsPerEntry)/b.Elapsed().Seconds(), "attempts/s")
}
