package usage

import (
	"testing"
	"time"
)

func TestBuildDateRange(t *testing.T) {
	today := time.Date(2026, time.July, 7, 0, 0, 0, 0, time.UTC)
	day := func(value string) time.Time {
		parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}

	tests := []struct {
		name      string
		startStr  string
		endStr    string
		days      int
		wantStart time.Time
		wantEnd   time.Time
		wantErr   bool
	}{
		{name: "both bounds", startStr: "2026-07-01", endStr: "2026-07-06", wantStart: day("2026-07-01"), wantEnd: day("2026-07-06")},
		{name: "only start defaults end to today", startStr: "2026-07-01", wantStart: day("2026-07-01"), wantEnd: today},
		{name: "only end defaults start to 30-day window", endStr: "2026-07-06", wantStart: day("2026-06-07"), wantEnd: day("2026-07-06")},
		{name: "neither uses days", days: 7, wantStart: day("2026-07-01"), wantEnd: today},
		{name: "non-positive days falls back to default", days: 0, wantStart: today.AddDate(0, 0, -(DefaultDateRangeDays - 1)), wantEnd: today},
		{name: "oversized days clamps to max", days: 1000, wantStart: today.AddDate(0, 0, -(MaxDateRangeDays - 1)), wantEnd: today},
		{name: "exactly max range accepted", startStr: "2025-07-08", endStr: "2026-07-07", wantStart: day("2025-07-08"), wantEnd: day("2026-07-07")},
		{name: "explicit range beyond max rejected", startStr: "2025-07-07", endStr: "2026-07-07", wantErr: true},
		{name: "inverted range rejected", startStr: "2026-07-06", endStr: "2026-07-01", wantErr: true},
		{name: "malformed start rejected", startStr: "garbage", wantErr: true},
		{name: "malformed end rejected", endStr: "07/06/2026", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := BuildDateRange(tt.startStr, tt.endStr, tt.days, time.UTC, today)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("BuildDateRange() = %s..%s, want error", start, end)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildDateRange() error = %v, want nil", err)
			}
			if !start.Equal(tt.wantStart) || !end.Equal(tt.wantEnd) {
				t.Fatalf("BuildDateRange() = %s..%s, want %s..%s", start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}
