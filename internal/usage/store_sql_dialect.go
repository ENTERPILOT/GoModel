package usage

import (
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// usageDialect holds the spellings SQLite and PostgreSQL genuinely disagree on
// for the usage table. Everything else in the store and reader is one query
// for both.
type usageDialect struct {
	sqlx.Dialect

	// idType is the primary key's column type. PostgreSQL has always stored
	// usage ids as UUID; keeping it means a fresh database matches the ones
	// already in the field.
	idType string

	// maxBindParameters caps the placeholders in one statement.
	maxBindParameters int

	// indexes are created on top of usageIndexes.
	indexes []string

	// timeExpr is the timestamp column as a comparable, orderable value, and
	// timeArg converts a bound to match it. SQLite holds RFC3339 text, plus
	// space-separated offset text in rows written before that, so it compares
	// epoch seconds of the normalized text; idx_usage_timestamp_epoch indexes
	// exactly that expression. PostgreSQL compares its TIMESTAMPTZ directly.
	timeExpr string
	timeArg  func(time.Time) any

	// like is the case-insensitive match operator. SQLite's LIKE already
	// ignores case for ASCII; PostgreSQL's does not, and needs ILIKE.
	like string

	// hasLabels selects rows whose labels can be expanded. labelMatch tests
	// for one label bound to its placeholder. labelSource expands each row's
	// labels into one row per label, read through labelValue.
	hasLabels   string
	labelMatch  string
	labelSource string
	labelValue  string

	// lockRows locks rows a pricing recalculation is about to rewrite.
	// SQLite needs nothing: its write transaction already holds the database.
	lockRows string
}

func usageDialectFor(dialect sqlx.Dialect) usageDialect {
	if dialect == sqlx.PostgreSQL {
		return usageDialect{
			Dialect:           dialect,
			idType:            "UUID",
			maxBindParameters: postgresMaxBindParameters,
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_usage_raw_data_gin ON usage USING GIN (raw_data)",
			},
			timeExpr:    "timestamp",
			timeArg:     func(t time.Time) any { return t.UTC() },
			like:        "ILIKE",
			hasLabels:   "jsonb_typeof(labels) = 'array'",
			labelMatch:  "jsonb_exists(labels, ?)",
			labelSource: "jsonb_array_elements_text(labels) AS label",
			labelValue:  "label",
			lockRows:    " FOR UPDATE",
		}
	}
	return usageDialect{
		Dialect:           dialect,
		idType:            "TEXT",
		maxBindParameters: sqliteMaxBindParameters,
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_usage_timestamp_epoch ON usage(" + sqliteTimestampEpochExpr + ")",
		},
		timeExpr:    sqliteTimestampEpochExpr,
		timeArg:     func(t time.Time) any { return t.UTC().Unix() },
		like:        "LIKE",
		hasLabels:   "labels IS NOT NULL",
		labelMatch:  "(labels IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(usage.labels) WHERE json_each.value = ?))",
		labelSource: "json_each(usage.labels) AS labels_each",
		labelValue:  "labels_each.value",
	}
}

const (
	sqliteTimestampTextExpr  = "REPLACE(timestamp, ' ', 'T')"
	sqliteTimestampEpochExpr = "unixepoch(" + sqliteTimestampTextExpr + ")"
)

// bucketExpr maps each row to the start of its epoch-aligned throughput
// bucket, shifted by offset seconds.
func (d usageDialect) bucketExpr(offset, bucketSeconds int64) string {
	if d.Dialect == sqlx.PostgreSQL {
		return fmt.Sprintf("(FLOOR((EXTRACT(EPOCH FROM timestamp) + %d) / %d) * %d - %d)::bigint", offset, bucketSeconds, bucketSeconds, offset)
	}
	return fmt.Sprintf("((%s + %d) / %d) * %d - %d", d.timeExpr, offset, bucketSeconds, bucketSeconds, offset)
}
