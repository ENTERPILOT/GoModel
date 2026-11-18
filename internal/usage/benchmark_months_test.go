package usage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOfDay(t *testing.T) {
	tests := []struct {
		name string
		zone string
		day  time.Time
		want string
	}{
		{name: "UTC", zone: "UTC", day: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), want: "2026-03-01T00:00:00Z"},
		{name: "plain midnight", zone: "Europe/Warsaw", day: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), want: "2026-02-28T23:00:00Z"},
		// time.Date answers 2023-09-30 23:00 -04, an hour before the gap.
		{name: "midnight skipped behind UTC", zone: "America/Asuncion", day: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), want: "2023-10-01T04:00:00Z"},
		{name: "midnight skipped ahead of UTC", zone: "Asia/Damascus", day: time.Date(2005, 4, 1, 0, 0, 0, 0, time.UTC), want: "2005-03-31T22:00:00Z"},
		{name: "midnight repeated behind UTC", zone: "America/Havana", day: time.Date(2020, 11, 1, 0, 0, 0, 0, time.UTC), want: "2020-11-01T04:00:00Z"},
		// time.Date answers the second midnight, 00:00 +02.
		{name: "midnight repeated ahead of UTC", zone: "Asia/Gaza", day: time.Date(2004, 10, 1, 0, 0, 0, 0, time.UTC), want: "2004-09-30T21:00:00Z"},
		{name: "clock set back from midnight", zone: "America/Asuncion", day: time.Date(2023, 3, 26, 0, 0, 0, 0, time.UTC), want: "2023-03-26T04:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := time.LoadLocation(tt.zone)
			require.NoError(t, err)
			got := startOfDay(tt.day.Year(), tt.day.Month(), tt.day.Day(), location)
			assert.Equal(t, tt.want, got.UTC().Format(time.RFC3339))
			assert.Equal(t, tt.day.Format(time.DateOnly), got.Format(time.DateOnly), "the day has begun")
			assert.Less(t, got.Add(-time.Nanosecond).Format(time.DateOnly), tt.day.Format(time.DateOnly), "and not before")
		})
	}
}

// A month ends where the next begins, so no instant is in two months or in
// none, also where a zone skips or repeats the midnight a month starts at.
func TestBenchmarkMonthBounds(t *testing.T) {
	tests := []struct {
		name        string
		zone        string
		first, last string
		wantStart   string
		wantEnd     string // exclusive
		wantMonths  []string
	}{
		{name: "UTC", zone: "UTC", first: "2026-01", last: "2026-03", wantStart: "2026-01-01T00:00:00Z", wantEnd: "2026-04-01T00:00:00Z", wantMonths: []string{"2026-01", "2026-02", "2026-03"}},
		{name: "into the next year", zone: "UTC", first: "2025-12", last: "2025-12", wantStart: "2025-12-01T00:00:00Z", wantEnd: "2026-01-01T00:00:00Z", wantMonths: []string{"2025-12"}},
		{name: "across a DST change", zone: "Europe/Warsaw", first: "2026-03", last: "2026-03", wantStart: "2026-02-28T23:00:00Z", wantEnd: "2026-03-31T22:00:00Z", wantMonths: []string{"2026-03"}},
		{name: "the month before a skipped midnight", zone: "America/Asuncion", first: "2023-09", last: "2023-09", wantStart: "2023-09-01T04:00:00Z", wantEnd: "2023-10-01T04:00:00Z", wantMonths: []string{"2023-09"}},
		{name: "the month of a skipped midnight", zone: "America/Asuncion", first: "2023-10", last: "2023-10", wantStart: "2023-10-01T04:00:00Z", wantEnd: "2023-11-01T03:00:00Z", wantMonths: []string{"2023-10"}},
		{name: "months around a skipped midnight", zone: "America/Asuncion", first: "2023-08", last: "2023-11", wantStart: "2023-08-01T04:00:00Z", wantEnd: "2023-12-01T03:00:00Z", wantMonths: []string{"2023-08", "2023-09", "2023-10", "2023-11"}},
		{name: "a skipped midnight ahead of UTC", zone: "Asia/Damascus", first: "2005-03", last: "2005-04", wantStart: "2005-02-28T22:00:00Z", wantEnd: "2005-04-30T21:00:00Z", wantMonths: []string{"2005-03", "2005-04"}},
		{name: "the month before a repeated midnight", zone: "America/Havana", first: "2020-10", last: "2020-10", wantStart: "2020-10-01T04:00:00Z", wantEnd: "2020-11-01T04:00:00Z", wantMonths: []string{"2020-10"}},
		// Ends at the first of the two midnights, 00:00 +03.
		{name: "the month before a repeated midnight ahead of UTC", zone: "Asia/Gaza", first: "2004-09", last: "2004-09", wantStart: "2004-08-31T21:00:00Z", wantEnd: "2004-09-30T21:00:00Z", wantMonths: []string{"2004-09"}},
		{name: "the month of a repeated midnight ahead of UTC", zone: "Asia/Gaza", first: "2004-10", last: "2004-10", wantStart: "2004-09-30T21:00:00Z", wantEnd: "2004-10-31T22:00:00Z", wantMonths: []string{"2004-10"}},
		// 2024-03-31 starts at 01:00 +00 in the Azores: 00:00 -01 is skipped.
		{name: "a last day starting at a skipped midnight", zone: "Atlantic/Azores", first: "2024-03", last: "2024-03", wantStart: "2024-03-01T01:00:00Z", wantEnd: "2024-04-01T00:00:00Z", wantMonths: []string{"2024-03"}},
		{name: "a last day starting at a skipped midnight ahead of UTC", zone: "Asia/Beirut", first: "2024-03", last: "2024-03", wantStart: "2024-02-29T22:00:00Z", wantEnd: "2024-03-31T21:00:00Z", wantMonths: []string{"2024-03"}},
		{name: "a last day starting at a repeated midnight", zone: "Atlantic/Azores", first: "2021-10", last: "2021-10", wantStart: "2021-10-01T00:00:00Z", wantEnd: "2021-11-01T01:00:00Z", wantMonths: []string{"2021-10"}},
		{name: "a last day starting at a repeated midnight ahead of UTC", zone: "Asia/Amman", first: "2014-10", last: "2014-10", wantStart: "2014-09-30T21:00:00Z", wantEnd: "2014-10-31T22:00:00Z", wantMonths: []string{"2014-10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := time.LoadLocation(tt.zone)
			require.NoError(t, err)
			first, err := time.Parse("2006-01", tt.first)
			require.NoError(t, err)
			last, err := time.Parse("2006-01", tt.last)
			require.NoError(t, err)

			params := UsageQueryParams{TimeZone: tt.zone}
			params.StartDate, params.EndDate = BenchmarkMonthBounds(first, last, location)
			assert.Equal(t, tt.wantStart, params.StartDate.UTC().Format(time.RFC3339))
			assert.Equal(t, tt.wantEnd, usageEndExclusive(params).UTC().Format(time.RFC3339))
			next, _ := BenchmarkMonthBounds(last.AddDate(0, 1, 0), last.AddDate(0, 1, 0), location)
			assert.True(t, next.Equal(usageEndExclusive(params)), "the next month starts at %s", next)

			months := []string{}
			for _, month := range EmptyBenchmark(BenchmarkParams{UsageQueryParams: params}).Months {
				months = append(months, month.Month)
			}
			assert.Equal(t, tt.wantMonths, months)
		})
	}
}
