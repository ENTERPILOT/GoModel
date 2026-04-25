package budget

import (
	"testing"
	"time"
)

func TestPeriodBoundsUsesConfiguredAnchors(t *testing.T) {
	settings := Settings{
		DailyResetHour:     6,
		DailyResetMinute:   30,
		WeeklyResetWeekday: int(time.Wednesday),
		WeeklyResetHour:    9,
		WeeklyResetMinute:  15,
		MonthlyResetDay:    31,
		MonthlyResetHour:   2,
		MonthlyResetMinute: 45,
	}
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)

	dailyStart, dailyEnd := PeriodBounds(now, PeriodDailySeconds, settings)
	if want := time.Date(2026, time.April, 25, 6, 30, 0, 0, time.UTC); !dailyStart.Equal(want) {
		t.Fatalf("daily start = %s, want %s", dailyStart, want)
	}
	if want := time.Date(2026, time.April, 26, 6, 30, 0, 0, time.UTC); !dailyEnd.Equal(want) {
		t.Fatalf("daily end = %s, want %s", dailyEnd, want)
	}

	weeklyStart, weeklyEnd := PeriodBounds(now, PeriodWeeklySeconds, settings)
	if want := time.Date(2026, time.April, 22, 9, 15, 0, 0, time.UTC); !weeklyStart.Equal(want) {
		t.Fatalf("weekly start = %s, want %s", weeklyStart, want)
	}
	if want := time.Date(2026, time.April, 29, 9, 15, 0, 0, time.UTC); !weeklyEnd.Equal(want) {
		t.Fatalf("weekly end = %s, want %s", weeklyEnd, want)
	}

	monthlyStart, monthlyEnd := PeriodBounds(now, PeriodMonthlySeconds, settings)
	if want := time.Date(2026, time.March, 31, 2, 45, 0, 0, time.UTC); !monthlyStart.Equal(want) {
		t.Fatalf("monthly start = %s, want %s", monthlyStart, want)
	}
	if want := time.Date(2026, time.April, 30, 2, 45, 0, 0, time.UTC); !monthlyEnd.Equal(want) {
		t.Fatalf("monthly end = %s, want %s", monthlyEnd, want)
	}
}
