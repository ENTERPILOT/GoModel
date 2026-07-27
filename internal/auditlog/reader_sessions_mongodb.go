package auditlog

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// mongoThreadKeyExpr groups entries into threads: the session id when present,
// otherwise the entry's own id (singleton threads for sessionless entries).
var mongoThreadKeyExpr = bson.D{{Key: "$cond", Value: bson.A{
	bson.D{{Key: "$eq", Value: bson.A{
		bson.D{{Key: "$ifNull", Value: bson.A{"$session_id", ""}}},
		"",
	}}},
	"$_id",
	"$session_id",
}}}

// GetSessions returns a paginated list of audit sessions ordered by latest
// activity, mirroring the SQL reader's window-function query with a $group
// aggregation.
func (r *MongoDBReader) GetSessions(ctx context.Context, params LogQueryParams) (*SessionListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	matchFilters, err := mongoLogMatchFilters(params)
	if err != nil {
		return nil, err
	}

	pipeline := bson.A{}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$addFields", Value: bson.D{{Key: "thread_key", Value: mongoThreadKeyExpr}}}},
		// Sort before $group so $first picks each thread's newest entry.
		bson.D{{Key: "$sort", Value: bson.D{{Key: "timestamp", Value: -1}, {Key: "_id", Value: -1}}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$thread_key"},
			{Key: "latest", Value: bson.D{{Key: "$first", Value: "$$ROOT"}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "first_ts", Value: bson.D{{Key: "$min", Value: "$timestamp"}}},
			{Key: "last_ts", Value: bson.D{{Key: "$max", Value: "$timestamp"}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "last_ts", Value: -1}, {Key: "_id", Value: -1}}}},
		bson.D{{Key: "$facet", Value: bson.D{
			{Key: "data", Value: bson.A{
				bson.D{{Key: "$skip", Value: offset}},
				bson.D{{Key: "$limit", Value: limit}},
			}},
			{Key: "total", Value: bson.A{
				bson.D{{Key: "$count", Value: "count"}},
			}},
		}}},
	)

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate audit sessions: %w", err)
	}
	defer cursor.Close(ctx)

	var facetResult struct {
		Data []struct {
			Latest  mongoLogRow `bson:"latest"`
			Count   int         `bson:"count"`
			FirstTS time.Time   `bson:"first_ts"`
			LastTS  time.Time   `bson:"last_ts"`
		} `bson:"data"`
		Total []struct {
			Count int `bson:"count"`
		} `bson:"total"`
	}
	if cursor.Next(ctx) {
		if err := cursor.Decode(&facetResult); err != nil {
			return nil, fmt.Errorf("failed to decode audit session facet result: %w", err)
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit session cursor: %w", err)
	}

	total := 0
	if len(facetResult.Total) > 0 {
		total = facetResult.Total[0].Count
	}

	sessions := make([]SessionSummary, 0, len(facetResult.Data))
	for _, row := range facetResult.Data {
		entry := row.Latest.toLogEntry()
		if entry == nil {
			continue
		}
		sessions = append(sessions, SessionSummary{
			SessionID:      entry.SessionID,
			Count:          row.Count,
			FirstTimestamp: row.FirstTS,
			LastTimestamp:  row.LastTS,
			Latest:         *entry,
		})
	}
	return &SessionListResult{Sessions: sessions, Total: total, Limit: limit, Offset: offset}, nil
}
