package auditlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// TestStoreWritesTimestampsInUTC pins the invariant the audit schema rests on:
// whatever zone a caller hands in, the column holds UTC.
//
// It matters because sqlx.Timestamp deliberately does *not* normalise on read —
// each driver's own zone is what callers already render — so the guarantee has
// to be established on the way in.
//
// The stakes differ per engine, which is why both are asserted. PostgreSQL's
// TIMESTAMPTZ stores an absolute instant, so a non-UTC time.Time still lands
// correctly and the check mostly guards against the column type changing.
// SQLite formats the value to text: dropping the UTC conversion there would
// write "2026-07-25T14:30:00+02:00", which sorts wrongly against every "…Z"
// row and would quietly break the date-range filters, since those compare the
// column as a string.
func TestStoreWritesTimestampsInUTC(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	tests := []struct {
		name string
		// written is the same instant expressed in different zones, except for
		// the winter case, which exists to cover the other side of a DST change.
		written      time.Time
		wantUTCClock string
		notLocalT    string
	}{
		{
			name:         "already UTC",
			written:      time.Date(2026, 7, 25, 12, 30, 0, 123456000, time.UTC),
			wantUTCClock: "12:30:00",
		},
		{
			name:         "summer offset +02:00",
			written:      time.Date(2026, 7, 25, 14, 30, 0, 123456000, warsaw),
			wantUTCClock: "12:30:00",
			notLocalT:    "14:30:00",
		},
		{
			name:         "winter offset +01:00",
			written:      time.Date(2026, 1, 15, 13, 30, 0, 123456000, warsaw),
			wantUTCClock: "12:30:00",
			notLocalT:    "13:30:00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
				store, err := NewSQLStore(context.Background(), db, 0)
				if err != nil {
					t.Fatalf("NewSQLStore: %v", err)
				}
				defer store.Close()

				ctx := context.Background()
				if err := store.WriteBatch(ctx, []*LogEntry{{
					ID: "utc-entry", Timestamp: tc.written, Provider: "openai", StatusCode: 200,
					Data: &LogData{Attempts: []AttemptSnapshot{
						{Seq: 1, Kind: "primary", StartedAt: tc.written},
					}},
				}}); err != nil {
					t.Fatalf("WriteBatch: %v", err)
				}

				// Both columns, read as stored rather than as the reader
				// interprets them.
				var entryStored, attemptStored string
				if err := db.QueryRow(ctx,
					"SELECT "+utcProjection(db, "timestamp")+" FROM audit_logs WHERE id = ?",
					"utc-entry").Scan(&entryStored); err != nil {
					t.Fatalf("read stored timestamp: %v", err)
				}
				if err := db.QueryRow(ctx,
					"SELECT "+utcProjection(db, "started_at")+" FROM audit_log_attempts WHERE audit_log_id = ?",
					"utc-entry").Scan(&attemptStored); err != nil {
					t.Fatalf("read stored started_at: %v", err)
				}

				for column, stored := range map[string]string{
					"audit_logs.timestamp":          entryStored,
					"audit_log_attempts.started_at": attemptStored,
				} {
					if !strings.Contains(stored, tc.wantUTCClock) {
						t.Errorf("%s = %q, want the %s UTC wall clock", column, stored, tc.wantUTCClock)
					}
					if tc.notLocalT != "" && strings.Contains(stored, tc.notLocalT) {
						t.Errorf("%s = %q, holds the caller's local wall clock %s", column, stored, tc.notLocalT)
					}
					// SQLite's text form must also carry the zone marker, or it
					// sorts wrongly against rows written in another zone.
					if db.Dialect() == sqlx.SQLite && !strings.HasSuffix(stored, "Z") {
						t.Errorf("%s = %q, want RFC3339 UTC text ending in Z", column, stored)
					}
				}

				// And the instant survives the round trip. PostgreSQL's
				// TIMESTAMPTZ holds microseconds, so the fixture stays inside
				// that precision.
				reader, err := NewSQLReader(db)
				if err != nil {
					t.Fatalf("NewSQLReader: %v", err)
				}
				entry, err := reader.GetLogByID(ctx, "utc-entry")
				if err != nil {
					t.Fatalf("GetLogByID: %v", err)
				}
				if !entry.Timestamp.Equal(tc.written) {
					t.Errorf("round-tripped timestamp = %s, want the same instant as %s",
						entry.Timestamp, tc.written)
				}
				if entry.Data == nil || len(entry.Data.Attempts) != 1 {
					t.Fatalf("attempts = %+v, want one hydrated attempt", entry.Data)
				}
				if !entry.Data.Attempts[0].StartedAt.Equal(tc.written) {
					t.Errorf("attempt started_at = %s, want the same instant as %s",
						entry.Data.Attempts[0].StartedAt, tc.written)
				}
			})
		})
	}
}

// utcProjection renders a timestamp column as its UTC wall clock.
//
// PostgreSQL formats TIMESTAMPTZ in the *session* time zone, so a plain ::text
// on a server that is not on UTC returns the local rendering of a correctly
// stored instant — which would fail this test on good data. SQLite already
// holds the UTC text that was written.
//
// The column name is a literal chosen here, never caller input, so building the
// statement around it is not a parameterisation this could use anyway: a column
// expression cannot be a bind parameter.
func utcProjection(db sqlx.DB, column string) string {
	if db.Dialect() == sqlx.PostgreSQL {
		return "(" + column + " AT TIME ZONE 'UTC')::text"
	}
	return column
}
