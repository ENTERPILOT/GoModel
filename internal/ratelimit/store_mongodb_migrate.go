package ratelimit

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// importLegacyCounters is the MongoDB half of the SQL importLegacyCounters:
// documents of the pre-writer rate_limit_counters collection move into
// rate_limit_window_snapshots under the empty writer, overwriting an earlier
// import of the same key, and the old collection is emptied rather than
// dropped so an older replica in a rolling upgrade keeps working. Only the
// documents read are deleted, so one an older replica writes meanwhile waits
// for the next start.
//
// The move runs in a transaction so a reset on an active replica cannot land
// between the read and the write and have its deleted counts written back:
// the reset's delete of a document read here makes the transaction conflict
// and retry from a fresh read. A standalone server has no transactions and
// keeps that narrow window; it only exists during the one-time upgrade.
func (s *MongoDBStore) importLegacyCounters(ctx context.Context) error {
	return s.inTransaction(ctx, "import legacy rate limit counters", s.moveLegacyCounters)
}

func (s *MongoDBStore) moveLegacyCounters(ctx context.Context) error {
	cursor, err := s.legacyCounters.Find(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("list legacy rate limit counters: %w", err)
	}
	defer cursor.Close(ctx)

	var writes []mongo.WriteModel
	var imported bson.A
	for cursor.Next(ctx) {
		// A malformed document is cleared with the rest: no release can
		// restore it, and keeping it would warn on every start.
		imported = append(imported, cursor.Current.Lookup("_id"))
		var doc counterDocument
		if err := cursor.Decode(&doc); err != nil {
			slog.Warn("rate limit counters: skipping malformed legacy snapshot", "error", err)
			continue
		}
		doc.Writer = ""
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(counterIdentityFilter(doc.WindowSnapshot)).
			SetUpdate(bson.D{{Key: "$set", Value: doc}}).
			SetUpsert(true))
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("iterate legacy rate limit counters: %w", err)
	}
	if len(imported) == 0 {
		return nil
	}
	if len(writes) > 0 {
		// Replicas starting together import the same documents; a duplicate
		// key means a peer's upsert inserted the row first.
		_, err := s.counters.BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false))
		if err != nil && !isOnlyDuplicateKeyErrors(err) {
			return fmt.Errorf("import legacy rate limit counters: %w", err)
		}
	}
	if _, err := s.legacyCounters.DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: imported}}}}); err != nil {
		return fmt.Errorf("clear legacy rate limit counters: %w", err)
	}
	return nil
}
