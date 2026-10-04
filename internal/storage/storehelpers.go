package storage

import (
	"maps"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
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

// MongoSwapSubfields builds a conditional update of string values inside the
// subdocument at field: the filter expression matches only while every key in
// current still holds its value there, and the update pipeline sets each of
// those keys to its value in next. Keys are addressed with $getField and
// $setField rather than dotted paths, so names containing "." or starting with
// "$" work too (MongoDB 5.0+). Compare key by key rather than the whole
// subdocument: a stored subdocument's key order is not stable.
func MongoSwapSubfields(field string, current, next map[string]string) (filter bson.M, update mongo.Pipeline) {
	keys := slices.Sorted(maps.Keys(current))
	conditions := make(bson.A, 0, len(keys))
	var value any = "$" + field
	for _, key := range keys {
		conditions = append(conditions, bson.M{"$eq": bson.A{
			bson.M{"$getField": bson.M{"field": bson.M{"$literal": key}, "input": "$" + field}},
			bson.M{"$literal": current[key]},
		}})
		value = bson.M{"$setField": bson.M{
			"field": bson.M{"$literal": key},
			"input": value,
			"value": bson.M{"$literal": next[key]},
		}}
	}
	return bson.M{"$expr": bson.M{"$and": conditions}}, mongo.Pipeline{{{Key: "$set", Value: bson.M{field: value}}}}
}
