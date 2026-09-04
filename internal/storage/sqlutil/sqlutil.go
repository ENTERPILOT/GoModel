// Package sqlutil provides small SQL query-building and value-conversion
// helpers shared by the SQL-backed stores and readers.
package sqlutil

import (
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// EscapeLikeWildcards escapes SQL LIKE/ILIKE wildcard characters in user input
// to prevent wildcard injection. Escapes \, %, and _.
func EscapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// BuildWhereClause joins condition strings into a SQL WHERE clause.
// Returns an empty string when conditions is empty.
func BuildWhereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// ClampLimitOffset normalises pagination parameters: limit defaults to
// defaultLimit when non-positive and is capped at maxLimit; offset floors at 0.
func ClampLimitOffset(limit, offset, defaultLimit, maxLimit int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ParseSQLiteTimestamp parses a SQLite text timestamp in the formats GoModel
// writes (RFC3339Nano, SQLite datetime with offset, or bare UTC seconds).
// Returns the zero time and false when no format matches.
func ParseSQLiteTimestamp(ts string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// UnixOrNil returns the UTC Unix timestamp for value, or nil when value is
// nil, for binding optional timestamps to nullable integer columns.
func UnixOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Unix()
}

// TimeFromUnix converts a stored Unix-seconds column into a UTC time,
// mapping a value JSON cannot represent (time.Time marshals only years 0-9999)
// to the zero time. A single corrupt timestamp — a row written while the host
// clock was wrong, or hand-edited — would otherwise make every API response
// carrying that row unserializable, failing the whole listing instead of the
// one column. Same tolerance as StringsFromJSON, for the same reason.
func TimeFromUnix(value int64) time.Time {
	t := time.Unix(value, 0).UTC()
	if year := t.Year(); year < 0 || year > 9999 {
		slog.Warn("dropping out-of-range stored timestamp", "unix_seconds", value)
		return time.Time{}
	}
	return t
}

// TimeFromUnixPtr converts an optional Unix timestamp to a *time.Time.
func TimeFromUnixPtr(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	t := TimeFromUnix(*value)
	return &t
}

// NullableString returns the trimmed value, or nil when blank, for binding
// optional strings to nullable text columns.
func NullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

// NullableJSONStrings marshals values to a JSON array for binding to a JSON
// column. Returns nil (SQL NULL) when values is empty or marshaling fails;
// failures are logged with ref identifying the row.
func NullableJSONStrings(values []string, ref string) any {
	if len(values) == 0 {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		slog.Warn("failed to marshal JSON string array column", "ref", ref, "error", err)
		return nil
	}
	return string(data)
}

// StringsFromJSON parses a JSON array column into a string slice, tolerating
// NULL, empty, and malformed values so one bad row cannot break listing;
// malformed values are logged with ref identifying the row.
func StringsFromJSON(raw string, ref string) []string {
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		slog.Warn("failed to unmarshal JSON string array column", "ref", ref, "error", err)
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

// DerefTrimmed converts an optional text column to a trimmed string.
func DerefTrimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
