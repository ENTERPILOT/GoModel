package usage

import (
	"time"

	"gomodel/internal/core"
)

const (
	// DefaultDateRangeDays is the usage window applied when a query gives no
	// explicit range.
	DefaultDateRangeDays = 30
	// MaxDateRangeDays caps a requested usage window.
	MaxDateRangeDays = 365
)

// BuildDateRange resolves an inclusive [start, end] day range from optional
// YYYY-MM-DD strings. When only one bound is given the other defaults (start:
// 30 days before end, end: today); when neither is given the range covers the
// last days days ending today.
func BuildDateRange(startStr, endStr string, days int, location *time.Location, today time.Time) (time.Time, time.Time, error) {
	var start, end time.Time
	var startParsed, endParsed bool

	if startStr != "" {
		t, err := time.ParseInLocation("2006-01-02", startStr, location)
		if err != nil {
			return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid start_date format, expected YYYY-MM-DD", nil)
		}
		start = t
		startParsed = true
	}
	if endStr != "" {
		t, err := time.ParseInLocation("2006-01-02", endStr, location)
		if err != nil {
			return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid end_date format, expected YYYY-MM-DD", nil)
		}
		end = t
		endParsed = true
	}

	if startParsed || endParsed {
		if !startParsed {
			start = end.AddDate(0, 0, -(DefaultDateRangeDays - 1))
		}
		if !endParsed {
			end = today
		}
	} else {
		days = NormalizeDateRangeDays(days)
		end = today
		start = today.AddDate(0, 0, -(days - 1))
	}

	if start.After(end) {
		return time.Time{}, time.Time{}, core.NewInvalidRequestError("start_date must be on or before end_date", nil)
	}
	// The cap guards explicit ranges too; days-derived ranges are already
	// clamped. AddDate keeps the comparison correct across DST transitions.
	if end.After(start.AddDate(0, 0, MaxDateRangeDays-1)) {
		return time.Time{}, time.Time{}, core.NewInvalidRequestError("date range must not exceed 365 days", nil)
	}
	return start, end, nil
}

// NormalizeDateRangeDays clamps days to [1, MaxDateRangeDays], defaulting to
// DefaultDateRangeDays when not positive.
func NormalizeDateRangeDays(days int) int {
	if days <= 0 {
		return DefaultDateRangeDays
	}
	return min(days, MaxDateRangeDays)
}
