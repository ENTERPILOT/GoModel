package storage

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Helpers shared by feature store backends (responsestore, conversationstore,
// ...) that persist snapshots with unix-seconds retention columns, where an
// expires_at of 0 means the row never expires.

// RowScanner is the single-row result shape shared by database/sql and pgx.
type RowScanner interface {
	Scan(dest ...any) error
}

// UnixTime converts a unix-seconds retention column into a time, mapping the
// 0 "never expires" sentinel to the zero time.
func UnixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// UnixOrZero converts a time into a unix-seconds retention column value,
// mapping the zero time to the 0 "never expires" sentinel.
func UnixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// MongoUnexpiredFilter matches the document with the given id whose expiry is
// unset or still in the future.
func MongoUnexpiredFilter(id string, now time.Time) bson.M {
	return bson.M{
		"_id": id,
		"$or": bson.A{
			bson.M{"expires_at": 0},
			bson.M{"expires_at": bson.M{"$gt": now.Unix()}},
		},
	}
}
