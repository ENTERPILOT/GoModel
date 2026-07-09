package auditlog

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// GetRequestStats returns time-bucketed status-class counts and per-provider
// latency aggregates for the dashboard charts.
func (r *MongoDBReader) GetRequestStats(ctx context.Context, params RequestStatsParams) (*RequestStats, error) {
	match := bson.D{}
	if tsFilter := mongoDateRangeFilter(params.QueryParams); tsFilter != nil {
		match = append(match, bson.E{Key: "timestamp", Value: tsFilter})
	}

	// Latency covers successful requests with a recorded duration that
	// actually reached a provider (local response-cache hits complete in
	// microseconds and would drag averages toward zero).
	statusIn := func(low, high int) bson.D {
		return bson.D{{Key: "$and", Value: bson.A{
			bson.D{{Key: "$gte", Value: bson.A{"$status_code", low}}},
			bson.D{{Key: "$lte", Value: bson.A{"$status_code", high}}},
		}}}
	}
	countWhen := func(cond bson.D) bson.D {
		return bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{cond, 1, 0}}}}}
	}
	latencyEligible := bson.D{{Key: "$and", Value: bson.A{
		statusIn(200, 299),
		bson.D{{Key: "$gt", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$duration_ns", 0}}}, 0}}},
		bson.D{{Key: "$not", Value: bson.A{
			bson.D{{Key: "$in", Value: bson.A{
				bson.D{{Key: "$ifNull", Value: bson.A{"$cache_type", ""}}},
				bson.A{CacheTypeExact, CacheTypeSemantic},
			}}},
		}}},
	}}}

	// Group by UTC hour and provider; foldRequestStats folds hours into the
	// requested bucket granularity.
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "hour", Value: bson.D{{Key: "$dateToString", Value: bson.D{
					{Key: "format", Value: "%Y-%m-%dT%H"},
					{Key: "date", Value: "$timestamp"},
				}}}},
				{Key: "provider", Value: bson.D{{Key: "$let", Value: bson.D{
					{Key: "vars", Value: bson.D{{Key: "name", Value: bson.D{{Key: "$trim", Value: bson.D{
						{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$provider_name", ""}}}},
					}}}}}},
					{Key: "in", Value: bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$eq", Value: bson.A{"$$name", ""}}},
						bson.D{{Key: "$trim", Value: bson.D{
							{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$provider", ""}}}},
						}}},
						"$$name",
					}}}},
				}}}},
			}},
			{Key: "requests", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "status_2xx", Value: countWhen(statusIn(200, 299))},
			{Key: "status_4xx", Value: countWhen(statusIn(400, 499))},
			{Key: "status_5xx", Value: countWhen(bson.D{{Key: "$gte", Value: bson.A{"$status_code", 500}}})},
			{Key: "duration_ns_sum", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				latencyEligible, bson.D{{Key: "$ifNull", Value: bson.A{"$duration_ns", 0}}}, 0,
			}}}}}},
			{Key: "duration_count", Value: countWhen(latencyEligible)},
		}}},
	}

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate audit request stats: %w", err)
	}
	defer cursor.Close(ctx)

	var raw []mongoStatsRow
	if err := cursor.All(ctx, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode audit request stats: %w", err)
	}

	stats := make([]statsRow, 0, len(raw))
	for _, row := range raw {
		hour, err := time.ParseInLocation(statsHourLayout, row.ID.Hour, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("failed to parse audit request stats hour %q: %w", row.ID.Hour, err)
		}
		stats = append(stats, statsRow{
			HourUTC:       hour,
			Provider:      row.ID.Provider,
			Requests:      row.Requests,
			Status2xx:     row.Status2xx,
			Status4xx:     row.Status4xx,
			Status5xx:     row.Status5xx,
			DurationNsSum: row.DurationNsSum,
			DurationCount: row.DurationCount,
		})
	}

	return foldRequestStats(stats, params), nil
}

type mongoStatsRow struct {
	ID struct {
		Hour     string `bson:"hour"`
		Provider string `bson:"provider"`
	} `bson:"_id"`
	Requests      int64 `bson:"requests"`
	Status2xx     int64 `bson:"status_2xx"`
	Status4xx     int64 `bson:"status_4xx"`
	Status5xx     int64 `bson:"status_5xx"`
	DurationNsSum int64 `bson:"duration_ns_sum"`
	DurationCount int64 `bson:"duration_count"`
}
