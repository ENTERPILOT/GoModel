package auditlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// TestStoreWritesTimestampsInUTC pins the invariant the whole audit schema
// rests on: whatever zone a caller hands in, the column holds UTC.
//
// It matters because sqlx.Timestamp deliberately does *not* normalise on read
// — each driver's own zone is what callers already render — so the guarantee
// has to be established on the way in. If a write ever bypassed
// Dialect.TimestampArg, SQLite would store a local-offset string that sorts
// wrongly against every other row, and the date-range filters would quietly
// return the wrong day.
func TestStoreWritesTimestampsInUTC(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		warsaw, err := time.LoadLocation("Europe/Warsaw")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		// 14:30+02:00 is 12:30 UTC. A local-zone write would store 14:30.
		written := time.Date(2026, 7, 25, 14, 30, 0, 123456000, warsaw)

		store, err := NewSQLStore(context.Background(), db, 0)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		if err := store.WriteBatch(ctx, []*LogEntry{{
			ID: "utc-entry", Timestamp: written, Provider: "openai", StatusCode: 200,
			Data: &LogData{Attempts: []AttemptSnapshot{{Seq: 1, Kind: "primary", StartedAt: written}}},
		}}); err != nil {
			t.Fatalf("WriteBatch: %v", err)
		}

		// The column itself, not the reader's interpretation of it.
		timestampExpr := "timestamp"
		if db.Dialect() == sqlx.PostgreSQL {
			timestampExpr = "timestamp::text"
		}
		var stored string
		if err := db.QueryRow(ctx,
			"SELECT "+timestampExpr+" FROM audit_logs WHERE id = ?", "utc-entry").Scan(&stored); err != nil {
			t.Fatalf("read stored timestamp: %v", err)
		}
		if !strings.Contains(stored, "12:30:00") {
			t.Errorf("stored timestamp = %q, want the 12:30 UTC wall clock, not the caller's 14:30", stored)
		}
		// SQLite writes RFC3339 text ending in Z; PostgreSQL renders a +00 offset.
		if !strings.HasSuffix(stored, "Z") && !strings.Contains(stored, "+00") {
			t.Errorf("stored timestamp = %q, want a UTC zone marker", stored)
		}

		// And the instant survives the round trip. PostgreSQL's TIMESTAMPTZ
		// holds microseconds, so the fixture stays inside that precision.
		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("NewSQLReader: %v", err)
		}
		entry, err := reader.GetLogByID(ctx, "utc-entry")
		if err != nil {
			t.Fatalf("GetLogByID: %v", err)
		}
		if !entry.Timestamp.Equal(written) {
			t.Errorf("round-tripped timestamp = %s, want the same instant as %s", entry.Timestamp, written)
		}
		if entry.Data == nil || len(entry.Data.Attempts) != 1 {
			t.Fatalf("attempts = %+v, want one hydrated attempt", entry.Data)
		}
		if !entry.Data.Attempts[0].StartedAt.Equal(written) {
			t.Errorf("attempt started_at = %s, want the same instant as %s",
				entry.Data.Attempts[0].StartedAt, written)
		}
	})
}
