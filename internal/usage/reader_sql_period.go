package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// periodExpr returns the expression labelling each row with its reporting
// period (day, ISO week, month or year) in the requested time zone, plus any
// arguments the expression binds.
//
// PostgreSQL converts with AT TIME ZONE. SQLite has no time zone database, so
// the range is split at the zone's UTC offset changes and each segment shifts
// by its own fixed offset.
func (r *SQLReader) periodExpr(ctx context.Context, params UsageQueryParams) (string, []any, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}
	if r.dialect.Dialect == sqlx.PostgreSQL {
		return pgPeriodExpr(interval, usageTimeZone(params)), nil, nil
	}

	location := usageLocation(params)
	if location == time.UTC {
		return sqlitePeriodExpr(interval, 0), nil, nil
	}

	rangeStart, rangeEnd, ok, err := r.sqliteGroupingRange(ctx, params)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return sqlitePeriodExpr(interval, 0), nil, nil
	}

	segments := sqliteTimeZoneSegments(rangeStart, rangeEnd, location)
	if len(segments) == 0 {
		return sqlitePeriodExpr(interval, 0), nil, nil
	}
	if len(segments) == 1 {
		return sqlitePeriodExpr(interval, segments[0].OffsetMinutes), nil, nil
	}

	var builder strings.Builder
	args := make([]any, 0, len(segments)-1)
	builder.WriteString("CASE")
	for _, segment := range segments {
		expr := sqlitePeriodExpr(interval, segment.OffsetMinutes)
		if segment.Until.IsZero() {
			builder.WriteString(" ELSE ")
			builder.WriteString(expr)
			continue
		}

		builder.WriteString(" WHEN ")
		builder.WriteString(sqliteTimestampEpochExpr)
		builder.WriteString(" < ? THEN ")
		builder.WriteString(expr)
		args = append(args, segment.Until.UTC().Unix())
	}
	builder.WriteString(" END")

	return builder.String(), args, nil
}

func pgPeriodExpr(interval string, timeZone string) string {
	zoneLiteral := pgQuoteLiteral(timeZone)

	switch interval {
	case "weekly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('week', timestamp AT TIME ZONE %s), 'IYYY-"W"IW')`, zoneLiteral)
	case "monthly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('month', timestamp AT TIME ZONE %s), 'YYYY-MM')`, zoneLiteral)
	case "yearly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('year', timestamp AT TIME ZONE %s), 'YYYY')`, zoneLiteral)
	default:
		return fmt.Sprintf(`to_char(DATE(timestamp AT TIME ZONE %s), 'YYYY-MM-DD')`, zoneLiteral)
	}
}

func pgQuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func sqlitePeriodExpr(interval string, offsetMinutes int) string {
	modifier := ""
	if offsetMinutes != 0 {
		modifier = fmt.Sprintf(", '%+d minutes'", offsetMinutes)
	}

	switch interval {
	case "weekly":
		return fmt.Sprintf(`strftime('%%G-W%%V', %s%s)`, sqliteTimestampTextExpr, modifier)
	case "monthly":
		return fmt.Sprintf(`strftime('%%Y-%%m', %s%s)`, sqliteTimestampTextExpr, modifier)
	case "yearly":
		return fmt.Sprintf(`strftime('%%Y', %s%s)`, sqliteTimestampTextExpr, modifier)
	default:
		return fmt.Sprintf(`DATE(%s%s)`, sqliteTimestampTextExpr, modifier)
	}
}

type sqliteTimeZoneSegment struct {
	Until         time.Time
	OffsetMinutes int
}

func (r *SQLReader) sqliteGroupingRange(ctx context.Context, params UsageQueryParams) (time.Time, time.Time, bool, error) {
	if !params.StartDate.IsZero() && !params.EndDate.IsZero() {
		return params.StartDate.UTC(), usageEndExclusive(params).UTC(), true, nil
	}

	var minTS, maxTS *int64
	conditions, args := r.dialect.dateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	query := `SELECT MIN(` + sqliteTimestampEpochExpr + `), MAX(` + sqliteTimestampEpochExpr + `) FROM usage` + sqlutil.BuildWhereClause(conditions)
	if err := r.db.QueryRow(ctx, query, args...).Scan(&minTS, &maxTS); err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("failed to determine sqlite usage range: %w", err)
	}
	if minTS == nil || maxTS == nil {
		return time.Time{}, time.Time{}, false, nil
	}

	start := time.Unix(*minTS, 0).UTC()
	end := time.Unix(*maxTS, 0).UTC()
	return start, end.Add(time.Second), true, nil
}

func sqliteTimeZoneSegments(startUTC time.Time, endUTC time.Time, location *time.Location) []sqliteTimeZoneSegment {
	if location == nil || !endUTC.After(startUTC) {
		return nil
	}

	segments := make([]sqliteTimeZoneSegment, 0, 4)
	current := startUTC.UTC()
	currentOffset := sqliteOffsetMinutes(current, location)

	for current.Before(endUTC) {
		transition, ok := sqliteNextOffsetTransition(current, endUTC, location, currentOffset)
		if !ok {
			segments = append(segments, sqliteTimeZoneSegment{OffsetMinutes: currentOffset})
			break
		}

		segments = append(segments, sqliteTimeZoneSegment{
			Until:         transition.UTC(),
			OffsetMinutes: currentOffset,
		})
		current = transition.UTC()
		currentOffset = sqliteOffsetMinutes(current, location)
	}

	return segments
}

func sqliteNextOffsetTransition(startUTC time.Time, endUTC time.Time, location *time.Location, startOffset int) (time.Time, bool) {
	for windowStart := startUTC.UTC(); windowStart.Before(endUTC); {
		windowEnd := windowStart.Add(time.Hour)
		if windowEnd.After(endUTC) {
			windowEnd = endUTC
		}

		sample := windowEnd.Add(-time.Second)
		if sample.Before(windowStart) {
			sample = windowStart
		}

		if sqliteOffsetMinutes(sample, location) != startOffset {
			for candidate := windowStart; candidate.Before(windowEnd); candidate = candidate.Add(time.Second) {
				if sqliteOffsetMinutes(candidate, location) != startOffset {
					return candidate, true
				}
			}
		}

		windowStart = windowEnd
	}

	return time.Time{}, false
}

func sqliteOffsetMinutes(ts time.Time, location *time.Location) int {
	_, offsetSeconds := ts.In(location).Zone()
	return offsetSeconds / 60
}
