package usage

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// MongoDBReader implements UsageReader for MongoDB.
type MongoDBReader struct {
	collection *mongo.Collection
}

// NewMongoDBReader creates a new MongoDB usage reader.
func NewMongoDBReader(database *mongo.Database) (*MongoDBReader, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBReader{collection: database.Collection("usage")}, nil
}

// GetSummary returns aggregated usage statistics for the given query parameters.
func (r *MongoDBReader) GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error) {
	pipeline := bson.A{}
	matchFilters := bson.D{}

	if tsFilter := mongoDateRangeFilter(params); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}
	if userPath, err := normalizeUsageUserPathFilter(params.UserPath); err != nil {
		return nil, err
	} else if userPath != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "user_path",
			Value: bson.D{
				{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
			},
		})
	}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: nil},
		{Key: "total_requests", Value: bson.D{{Key: "$sum", Value: 1}}},
		{Key: "total_input", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
		{Key: "total_output", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
		{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
		{Key: "total_input_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$input_cost", 0}}}}}},
		{Key: "total_output_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$output_cost", 0}}}}}},
		{Key: "total_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$total_cost", 0}}}}}},
		{Key: "has_costs", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$gt", Value: bson.A{"$total_cost", nil}}}, 1, 0}}}}}},
	}}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage summary: %w", err)
	}
	defer cursor.Close(ctx)

	summary := &UsageSummary{}
	if cursor.Next(ctx) {
		var result struct {
			TotalRequests   int     `bson:"total_requests"`
			TotalInput      int64   `bson:"total_input"`
			TotalOutput     int64   `bson:"total_output"`
			TotalTokens     int64   `bson:"total_tokens"`
			TotalInputCost  float64 `bson:"total_input_cost"`
			TotalOutputCost float64 `bson:"total_output_cost"`
			TotalCost       float64 `bson:"total_cost"`
			HasCosts        int     `bson:"has_costs"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode usage summary: %w", err)
		}
		summary.TotalRequests = result.TotalRequests
		summary.TotalInput = result.TotalInput
		summary.TotalOutput = result.TotalOutput
		summary.TotalTokens = result.TotalTokens
		if result.HasCosts > 0 {
			summary.TotalInputCost = &result.TotalInputCost
			summary.TotalOutputCost = &result.TotalOutputCost
			summary.TotalCost = &result.TotalCost
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage summary cursor: %w", err)
	}

	return summary, nil
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *MongoDBReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	pipeline := bson.A{}
	matchFilters := bson.D{}

	if tsFilter := mongoDateRangeFilter(params); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}
	if userPath, err := normalizeUsageUserPathFilter(params.UserPath); err != nil {
		return nil, err
	} else if userPath != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "user_path",
			Value: bson.D{
				{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
			},
		})
	}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: bson.D{
			{Key: "model", Value: "$model"},
			{Key: "provider", Value: "$provider"},
		}},
		{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
		{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
		{Key: "input_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$input_cost", 0}}}}}},
		{Key: "output_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$output_cost", 0}}}}}},
		{Key: "total_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$total_cost", 0}}}}}},
		{Key: "has_costs", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$gt", Value: bson.A{"$total_cost", nil}}}, 1, 0}}}}}},
	}}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage by model: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]ModelUsage, 0)
	for cursor.Next(ctx) {
		var row struct {
			ID struct {
				Model    string `bson:"model"`
				Provider string `bson:"provider"`
			} `bson:"_id"`
			InputTokens  int64   `bson:"input_tokens"`
			OutputTokens int64   `bson:"output_tokens"`
			InputCost    float64 `bson:"input_cost"`
			OutputCost   float64 `bson:"output_cost"`
			TotalCost    float64 `bson:"total_cost"`
			HasCosts     int     `bson:"has_costs"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode usage by model row: %w", err)
		}
		m := ModelUsage{
			Model:        row.ID.Model,
			Provider:     row.ID.Provider,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
		}
		if row.HasCosts > 0 {
			m.InputCost = &row.InputCost
			m.OutputCost = &row.OutputCost
			m.TotalCost = &row.TotalCost
		}
		result = append(result, m)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by model cursor: %w", err)
	}

	return result, nil
}

// GetUsageLog returns a paginated list of individual usage log entries.
func (r *MongoDBReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	matchFilters := bson.D{}

	if tsFilter := mongoDateRangeFilter(params.UsageQueryParams); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}

	if params.Model != "" {
		matchFilters = append(matchFilters, bson.E{Key: "model", Value: params.Model})
	}
	if params.Provider != "" {
		matchFilters = append(matchFilters, bson.E{Key: "provider", Value: params.Provider})
	}
	if userPath, err := normalizeUsageUserPathFilter(params.UserPath); err != nil {
		return nil, err
	} else if userPath != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "user_path",
			Value: bson.D{
				{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
			},
		})
	}
	if params.Search != "" {
		regex := bson.D{{Key: "$regex", Value: params.Search}, {Key: "$options", Value: "i"}}
		matchFilters = append(matchFilters, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "model", Value: regex}},
			bson.D{{Key: "provider", Value: regex}},
			bson.D{{Key: "request_id", Value: regex}},
			bson.D{{Key: "provider_id", Value: regex}},
		}})
	}

	pipeline := bson.A{}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	pipeline = append(pipeline, bson.D{{Key: "$facet", Value: bson.D{
		{Key: "data", Value: bson.A{
			bson.D{{Key: "$sort", Value: bson.D{{Key: "timestamp", Value: -1}}}},
			bson.D{{Key: "$skip", Value: offset}},
			bson.D{{Key: "$limit", Value: limit}},
		}},
		{Key: "total", Value: bson.A{
			bson.D{{Key: "$count", Value: "count"}},
		}},
	}}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage log: %w", err)
	}
	defer cursor.Close(ctx)

	var facetResult struct {
		Data []struct {
			ID                     string         `bson:"_id"`
			RequestID              string         `bson:"request_id"`
			ProviderID             string         `bson:"provider_id"`
			Timestamp              time.Time      `bson:"timestamp"`
			Model                  string         `bson:"model"`
			Provider               string         `bson:"provider"`
			Endpoint               string         `bson:"endpoint"`
			UserPath               string         `bson:"user_path"`
			InputTokens            int            `bson:"input_tokens"`
			OutputTokens           int            `bson:"output_tokens"`
			TotalTokens            int            `bson:"total_tokens"`
			InputCost              *float64       `bson:"input_cost"`
			OutputCost             *float64       `bson:"output_cost"`
			TotalCost              *float64       `bson:"total_cost"`
			RawData                map[string]any `bson:"raw_data"`
			CostsCalculationCaveat string         `bson:"costs_calculation_caveat"`
		} `bson:"data"`
		Total []struct {
			Count int `bson:"count"`
		} `bson:"total"`
	}

	if cursor.Next(ctx) {
		if err := cursor.Decode(&facetResult); err != nil {
			return nil, fmt.Errorf("failed to decode usage log facet result: %w", err)
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage log cursor: %w", err)
	}

	total := 0
	if len(facetResult.Total) > 0 {
		total = facetResult.Total[0].Count
	}

	entries := make([]UsageLogEntry, 0, len(facetResult.Data))
	for _, row := range facetResult.Data {
		entries = append(entries, UsageLogEntry{
			ID:                     row.ID,
			RequestID:              row.RequestID,
			ProviderID:             row.ProviderID,
			Timestamp:              row.Timestamp,
			Model:                  row.Model,
			Provider:               row.Provider,
			Endpoint:               row.Endpoint,
			UserPath:               row.UserPath,
			InputTokens:            row.InputTokens,
			OutputTokens:           row.OutputTokens,
			TotalTokens:            row.TotalTokens,
			InputCost:              row.InputCost,
			OutputCost:             row.OutputCost,
			TotalCost:              row.TotalCost,
			RawData:                row.RawData,
			CostsCalculationCaveat: row.CostsCalculationCaveat,
		})
	}

	return &UsageLogResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// mongoDateRangeFilter returns a bson.D timestamp filter for the given date range.
// Returns nil if no date filtering is needed.
func mongoDateRangeFilter(params UsageQueryParams) bson.D {
	startZero := params.StartDate.IsZero()
	endZero := params.EndDate.IsZero()

	if !startZero && !endZero {
		return bson.D{{Key: "$gte", Value: params.StartDate.UTC()}, {Key: "$lt", Value: usageEndExclusive(params).UTC()}}
	}
	if !startZero {
		return bson.D{{Key: "$gte", Value: params.StartDate.UTC()}}
	}
	if !endZero {
		return bson.D{{Key: "$lt", Value: usageEndExclusive(params).UTC()}}
	}
	return nil
}

func mongoDateFormat(interval string) string {
	switch interval {
	case "weekly":
		return "%G-W%V"
	case "monthly":
		return "%Y-%m"
	case "yearly":
		return "%Y"
	default:
		return "%Y-%m-%d"
	}
}

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *MongoDBReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}

	pipeline := bson.A{}
	matchFilters := bson.D{}

	if tsFilter := mongoDateRangeFilter(params); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}
	if userPath, err := normalizeUsageUserPathFilter(params.UserPath); err != nil {
		return nil, err
	} else if userPath != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "user_path",
			Value: bson.D{
				{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
			},
		})
	}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	dateFormat := mongoDateFormat(interval)

	pipeline = append(pipeline,
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: dateFormat},
				{Key: "date", Value: "$timestamp"},
				{Key: "timezone", Value: usageTimeZone(params)},
			}}}},
			{Key: "requests", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
			{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
			{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
			{Key: "input_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$input_cost", 0}}}}}},
			{Key: "output_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$output_cost", 0}}}}}},
			{Key: "total_cost", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$total_cost", 0}}}}}},
			{Key: "has_costs", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$gt", Value: bson.A{"$total_cost", nil}}}, 1, 0}}}}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	)

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily usage: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]DailyUsage, 0)
	for cursor.Next(ctx) {
		var row struct {
			Date         string  `bson:"_id"`
			Requests     int     `bson:"requests"`
			InputTokens  int64   `bson:"input_tokens"`
			OutputTokens int64   `bson:"output_tokens"`
			TotalTokens  int64   `bson:"total_tokens"`
			InputCost    float64 `bson:"input_cost"`
			OutputCost   float64 `bson:"output_cost"`
			TotalCost    float64 `bson:"total_cost"`
			HasCosts     int     `bson:"has_costs"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode daily usage row: %w", err)
		}
		d := DailyUsage{
			Date:         row.Date,
			Requests:     row.Requests,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
		}
		if row.HasCosts > 0 {
			d.InputCost = &row.InputCost
			d.OutputCost = &row.OutputCost
			d.TotalCost = &row.TotalCost
		}
		result = append(result, d)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating daily usage cursor: %w", err)
	}

	return result, nil
}
