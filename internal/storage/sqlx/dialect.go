package sqlx

import (
	"strings"
	"time"
)

// Portable DDL type tokens.
//
// The two backends declare the same logical column with different type names.
// A store writes the token; the dialect expands it. Types that are already
// spelled the same on both backends (TEXT, INTEGER for genuinely 32-bit
// counters) are written literally and need no token.
//
// The expansions reproduce the column types the hand-written stores already
// used, so `CREATE TABLE IF NOT EXISTS` stays a no-op against existing
// databases and a fresh database gets a byte-identical schema. Both JSON
// tokens exist for that reason: some SQLite tables declared JSON columns as
// `JSON` and others as `TEXT`, and preserving each avoids changing the column
// affinity of tables already in the field.
const (
	// TypeInt64 is a 64-bit integer column: unix timestamps, byte counts.
	TypeInt64 = "{int64}"

	// TypeBool is a boolean column. SQLite has no boolean type and stores
	// 0/1 in an INTEGER; both drivers bind and scan Go bool against it.
	TypeBool = "{bool}"

	// TypeFloat is a floating-point column: costs, scores.
	TypeFloat = "{float}"

	// TypeJSON is a JSON column that SQLite declares as JSON.
	TypeJSON = "{json}"

	// TypeJSONText is a JSON column that SQLite declares as TEXT.
	TypeJSONText = "{json_text}"

	// TypeTimestamp is an absolute-time column. Note that the two engines do
	// not agree on how a value binds to it: see TimestampArg.
	TypeTimestamp = "{timestamp}"

	// TypeSerialPK is an auto-assigned integer primary key.
	TypeSerialPK = "{serial_pk}"
)

var typeExpansions = map[Dialect]*strings.Replacer{
	SQLite: strings.NewReplacer(
		TypeInt64, "INTEGER",
		TypeBool, "INTEGER",
		TypeFloat, "REAL",
		TypeJSON, "JSON",
		TypeJSONText, "TEXT",
		TypeTimestamp, "DATETIME",
		TypeSerialPK, "INTEGER PRIMARY KEY AUTOINCREMENT",
	),
	PostgreSQL: strings.NewReplacer(
		TypeInt64, "BIGINT",
		TypeBool, "BOOLEAN",
		TypeFloat, "DOUBLE PRECISION",
		TypeJSON, "JSONB",
		TypeJSONText, "JSONB",
		TypeTimestamp, "TIMESTAMPTZ",
		TypeSerialPK, "BIGSERIAL PRIMARY KEY",
	),
}

// ExpandTypes replaces portable type tokens with this dialect's column types.
// Statements containing no tokens are returned unchanged.
func (d Dialect) ExpandTypes(statement string) string {
	replacer, ok := typeExpansions[d]
	if !ok {
		return statement
	}
	return replacer.Replace(statement)
}

// TimestampArg converts a time into the form this dialect's TypeTimestamp
// column expects.
//
// PostgreSQL binds a time.Time to TIMESTAMPTZ directly. SQLite has no real
// date type: these columns hold RFC3339 text, which is what the readers parse
// back, so the store must keep writing text rather than letting the driver
// choose a representation.
func (d Dialect) TimestampArg(t time.Time) any {
	if d == SQLite {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return t.UTC()
}

// NullableTimestampArg is TimestampArg, mapping the zero time to SQL NULL.
func (d Dialect) NullableTimestampArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return d.TimestampArg(t)
}
