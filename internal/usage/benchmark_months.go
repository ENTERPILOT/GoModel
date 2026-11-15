package usage

import "time"

// BenchmarkMonthBounds returns the StartDate and EndDate of a benchmark over
// the calendar months from first to last in location; only the year and month
// of first and last are read. The range starts at the first instant of
// first's first day and ends at the first instant of the month after last.
// Readers end a range one calendar day after EndDate, on EndDate's own clock,
// so EndDate is that instant one day earlier in UTC, whose days are all 24
// hours: on location's clock the day before may start at a midnight that is
// skipped or repeated, and the end would move by the hour.
func BenchmarkMonthBounds(first, last time.Time, location *time.Location) (start, end time.Time) {
	start = startOfDay(first.Year(), first.Month(), 1, location)
	next := startOfDay(last.Year(), last.Month()+1, 1, location)
	return start, next.UTC().AddDate(0, 0, -1)
}

// startOfDay returns the first instant of a calendar day in location: the
// earliest one whose clock reads the day's midnight or later. time.Date does
// not: where a clock change skips midnight it returns the hour before the gap
// in zones behind UTC, and where midnight repeats it returns the second one
// in zones ahead of UTC.
func startOfDay(year int, month time.Month, day int, location *time.Location) time.Time {
	midnight := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	// No zone is a day ahead of UTC, so the walk starts before the day does.
	t := midnight.AddDate(0, 0, -1).In(location)
	for {
		_, offset := t.Zone()
		start := midnight.Add(-time.Duration(offset) * time.Second).In(location)
		if start.Before(t) {
			// This offset began with its clock already past midnight.
			return t
		}
		_, end := t.ZoneBounds()
		if end.IsZero() || start.Before(end) {
			return start
		}
		t = end
	}
}
