package auditlog

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// GetLastUsedByAuthKeys returns the newest audit timestamp per auth key id,
// mirroring the SQL reader's GROUP BY with a $group aggregation.
func (r *MongoDBReader) GetLastUsedByAuthKeys(ctx context.Context, keyIDs []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}

	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "auth_key_id", Value: bson.D{{Key: "$in", Value: keyIDs}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$auth_key_id"},
			{Key: "last", Value: bson.D{{Key: "$max", Value: "$timestamp"}}},
		}}},
	}

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate auth key last used: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []struct {
		ID   string    `bson:"_id"`
		Last time.Time `bson:"last"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("error iterating auth key last used cursor: %w", err)
	}
	for _, row := range rows {
		result[row.ID] = row.Last
	}
	return result, nil
}
