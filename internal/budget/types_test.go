package budget

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestApplySettingValueIgnoresUnknownNonInteger(t *testing.T) {
	settings := DefaultSettings()
	if err := applySettingValue(&settings, "unknown_setting", "not-an-int"); err != nil {
		t.Fatalf("applySettingValue() error = %v, want nil for unknown setting", err)
	}
}

func TestApplySettingValueRejectsKnownNonInteger(t *testing.T) {
	settings := DefaultSettings()
	err := applySettingValue(&settings, settingDailyResetHour, "not-an-int")
	if err == nil {
		t.Fatal("applySettingValue() error = nil, want parse error")
	}
	if !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("applySettingValue() error = %v", err)
	}
}

func TestNormalizeBudgetRejectsNonFiniteAmount(t *testing.T) {
	tests := []struct {
		name   string
		amount float64
	}{
		{name: "nan", amount: math.NaN()},
		{name: "positive infinity", amount: math.Inf(1)},
		{name: "negative infinity", amount: math.Inf(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeBudget(Budget{
				UserPath:      "/team",
				PeriodSeconds: PeriodDailySeconds,
				Amount:        tt.amount,
			})
			if err == nil {
				t.Fatal("NormalizeBudget() error = nil, want non-finite amount error")
			}
			if !strings.Contains(err.Error(), "amount must be a finite number greater than 0") {
				t.Fatalf("NormalizeBudget() error = %v, want finite amount validation", err)
			}
		})
	}
}

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

	tests := []struct {
		name      string
		now       time.Time
		period    int64
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "daily exactly at anchor",
			now:       time.Date(2026, time.April, 25, 6, 30, 0, 0, time.UTC),
			period:    PeriodDailySeconds,
			wantStart: time.Date(2026, time.April, 25, 6, 30, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.April, 26, 6, 30, 0, 0, time.UTC),
		},
		{
			name:      "weekly on anchor weekday before anchor",
			now:       time.Date(2026, time.April, 22, 9, 14, 59, 0, time.UTC),
			period:    PeriodWeeklySeconds,
			wantStart: time.Date(2026, time.April, 15, 9, 15, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.April, 22, 9, 15, 0, 0, time.UTC),
		},
		{
			name:      "weekly exactly at anchor",
			now:       time.Date(2026, time.April, 22, 9, 15, 0, 0, time.UTC),
			period:    PeriodWeeklySeconds,
			wantStart: time.Date(2026, time.April, 22, 9, 15, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.April, 29, 9, 15, 0, 0, time.UTC),
		},
		{
			name:      "weekly on anchor weekday after anchor",
			now:       time.Date(2026, time.April, 22, 9, 15, 1, 0, time.UTC),
			period:    PeriodWeeklySeconds,
			wantStart: time.Date(2026, time.April, 22, 9, 15, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.April, 29, 9, 15, 0, 0, time.UTC),
		},
		{
			name:      "monthly day 31 before non-leap February anchor",
			now:       time.Date(2026, time.February, 28, 2, 44, 59, 0, time.UTC),
			period:    PeriodMonthlySeconds,
			wantStart: time.Date(2026, time.January, 31, 2, 45, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.February, 28, 2, 45, 0, 0, time.UTC),
		},
		{
			name:      "monthly day 31 at non-leap February anchor",
			now:       time.Date(2026, time.February, 28, 2, 45, 0, 0, time.UTC),
			period:    PeriodMonthlySeconds,
			wantStart: time.Date(2026, time.February, 28, 2, 45, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.March, 31, 2, 45, 0, 0, time.UTC),
		},
		{
			name:      "monthly day 31 at leap February anchor",
			now:       time.Date(2024, time.February, 29, 2, 45, 0, 0, time.UTC),
			period:    PeriodMonthlySeconds,
			wantStart: time.Date(2024, time.February, 29, 2, 45, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, time.March, 31, 2, 45, 0, 0, time.UTC),
		},
		{
			name:      "monthly day 31 clamps April",
			now:       time.Date(2026, time.April, 30, 3, 0, 0, 0, time.UTC),
			period:    PeriodMonthlySeconds,
			wantStart: time.Date(2026, time.April, 30, 2, 45, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.May, 31, 2, 45, 0, 0, time.UTC),
		},
		{
			name:      "monthly day 31 clamps June",
			now:       time.Date(2026, time.June, 30, 3, 0, 0, 0, time.UTC),
			period:    PeriodMonthlySeconds,
			wantStart: time.Date(2026, time.June, 30, 2, 45, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, time.July, 31, 2, 45, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := PeriodBounds(tt.now, tt.period, settings)
			if !start.Equal(tt.wantStart) {
				t.Fatalf("start = %s, want %s", start, tt.wantStart)
			}
			if !end.Equal(tt.wantEnd) {
				t.Fatalf("end = %s, want %s", end, tt.wantEnd)
			}
		})
	}
}
