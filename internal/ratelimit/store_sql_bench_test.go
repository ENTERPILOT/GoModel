package ratelimit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// BenchmarkSQLStoreSaveCounters measures one flush of changed keys on a file
// SQLite store that already holds other writers' live rows, as a replica of a
// cluster with per-user rules sees it.
func BenchmarkSQLStoreSaveCounters(b *testing.B) {
	for _, tc := range []struct{ peerRows, keys int }{
		{peerRows: 20_000, keys: 1},
		{peerRows: 20_000, keys: 1_000},
	} {
		b.Run(fmt.Sprintf("peers=%d/keys=%d", tc.peerRows, tc.keys), func(b *testing.B) {
			ctx := context.Background()
			sqlite, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(b.TempDir(), "bench.db")})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = sqlite.Close() })
			db, err := sqlx.NewSQLite(sqlite.DB())
			if err != nil {
				b.Fatal(err)
			}
			store, err := NewSQLStore(ctx, db)
			if err != nil {
				b.Fatal(err)
			}
			if err := store.SaveCounters(ctx, benchSnapshots("peer", tc.peerRows)); err != nil {
				b.Fatal(err)
			}
			own := benchSnapshots("self", tc.keys)
			b.ReportAllocs()
			for b.Loop() {
				for i := range own {
					own[i].RequestsCurrent++
				}
				if err := store.SaveCounters(ctx, own); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchSnapshots(writer string, n int) []WindowSnapshot {
	out := make([]WindowSnapshot, n)
	for i := range out {
		out[i] = WindowSnapshot{
			Scope: string(ScopeUserPath), Subject: "/users", Partition: fmt.Sprintf("/users/%d", i),
			PeriodSeconds: PeriodHourSeconds, Writer: writer, Epoch: "e1",
			RequestsWindowStart: 1700000000, RequestsCurrent: 1,
			TokensWindowStart: 1700000000, TokensCurrent: 10,
		}
	}
	return out
}
