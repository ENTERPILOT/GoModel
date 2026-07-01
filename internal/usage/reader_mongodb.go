package usage

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDBReader implements UsageReader for MongoDB.
type MongoDBReader struct {
	collection *mongo.Collection
}

type mongoUsageLogRow struct {
	ID                     string         `bson:"_id"`
	RequestID              string         `bson:"request_id"`
	ProviderID             string         `bson:"provider_id"`
	Timestamp              time.Time      `bson:"timestamp"`
	Model                  string         `bson:"model"`
	Provider               string         `bson:"provider"`
	ProviderName           string         `bson:"provider_name"`
	Endpoint               string         `bson:"endpoint"`
	UserPath               string         `bson:"user_path"`
	CacheType              string         `bson:"cache_type"`
	Labels                 []string       `bson:"labels"`
	InputTokens            int            `bson:"input_tokens"`
	OutputTokens           int            `bson:"output_tokens"`
	TotalTokens            int            `bson:"total_tokens"`
	InputCost              *float64       `bson:"input_cost"`
	OutputCost             *float64       `bson:"output_cost"`
	TotalCost              *float64       `bson:"total_cost"`
	CostSource             string         `bson:"cost_source"`
	RawData                map[string]any `bson:"raw_data"`
	CostsCalculationCaveat string         `bson:"costs_calculation_caveat"`
}

func (row mongoUsageLogRow) toUsageLogEntry() UsageLogEntry {
	return UsageLogEntry{
		ID:                     row.ID,
		RequestID:              row.RequestID,
		ProviderID:             row.ProviderID,
		Timestamp:              row.Timestamp,
		Model:                  row.Model,
		Provider:               row.Provider,
		ProviderName:           displayUsageProviderName(row.ProviderName, row.Provider),
		Endpoint:               row.Endpoint,
		UserPath:               row.UserPath,
		CacheType:              normalizeCacheType(row.CacheType),
		Labels:                 row.Labels,
		InputTokens:            row.InputTokens,
		OutputTokens:           row.OutputTokens,
		TotalTokens:            row.TotalTokens,
		InputCost:              row.InputCost,
		OutputCost:             row.OutputCost,
		TotalCost:              row.TotalCost,
		CostSource:             row.CostSource,
		RawData:                row.RawData,
		CostsCalculationCaveat: row.CostsCalculationCaveat,
	}
}

// NewMongoDBReader creates a new MongoDB usage reader.
// mongoCostSum sums a nullable cost field, treating missing values as zero.
func mongoCostSum(field string) bson.D {
	return bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{field, 0}}}}}
}

// mongoCostPresenceCount counts documents where a nullable cost field is set,
// so decoders can distinguish an all-null aggregate from a zero total.
func mongoCostPresenceCount(field string) bson.D {
	return bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$gt", Value: bson.A{field, nil}}}, 1, 0}}}}}
}

// costPtr exposes an aggregated cost only when at least one document carried
// that cost, mirroring the nullable columns of the SQL backends.
func costPtr(present int, value float64) *float64 {
	if present > 0 {
		return &value
	}
	return nil
}

func NewMongoDBReader(database *mongo.Database) (*MongoDBReader, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBReader{collection: database.Collection("usage")}, nil
}

// GetSummary returns aggregated usage statistics for the given query parameters.
func (r *MongoDBReader) GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error) {
	pipeline := bson.A{}
	matchFilters, err := mongoUsageMatchFilters(params)
	if err != nil {
		return nil, err
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
		{Key: "total_input_cost", Value: mongoCostSum("$input_cost")},
		{Key: "total_output_cost", Value: mongoCostSum("$output_cost")},
		{Key: "total_cost", Value: mongoCostSum("$total_cost")},
		{Key: "has_input_cost", Value: mongoCostPresenceCount("$input_cost")},
		{Key: "has_output_cost", Value: mongoCostPresenceCount("$output_cost")},
		{Key: "has_total_cost", Value: mongoCostPresenceCount("$total_cost")},
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
			HasInputCost    int     `bson:"has_input_cost"`
			HasOutputCost   int     `bson:"has_output_cost"`
			HasTotalCost    int     `bson:"has_total_cost"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode usage summary: %w", err)
		}
		summary.TotalRequests = result.TotalRequests
		summary.TotalInput = result.TotalInput
		summary.TotalOutput = result.TotalOutput
		summary.TotalTokens = result.TotalTokens
		summary.TotalInputCost = costPtr(result.HasInputCost, result.TotalInputCost)
		summary.TotalOutputCost = costPtr(result.HasOutputCost, result.TotalOutputCost)
		summary.TotalCost = costPtr(result.HasTotalCost, result.TotalCost)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage summary cursor: %w", err)
	}

	if err := r.accumulateInputSegments(ctx, matchFilters, summary); err != nil {
		return nil, err
	}

	return summary, nil
}

// accumulateInputSegments streams the matched documents and folds each row's
// provider prompt-cache split into the summary, reusing EntryInputSegments. The
// $group aggregate above cannot return per-document raw_data, so this is a
// second pass over the same filter projecting only the needed fields. This is
// the dashboard summary path, not a request hot path.
func (r *MongoDBReader) accumulateInputSegments(ctx context.Context, matchFilters bson.D, summary *UsageSummary) error {
	projection := bson.D{
		{Key: "input_tokens", Value: 1},
		{Key: "provider", Value: 1},
		{Key: "raw_data", Value: 1},
	}
	cursor, err := r.collection.Find(ctx, matchFilters, options.Find().SetProjection(projection))
	if err != nil {
		return fmt.Errorf("failed to query usage input segments: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var row struct {
			InputTokens int            `bson:"input_tokens"`
			Provider    string         `bson:"provider"`
			RawData     map[string]any `bson:"raw_data"`
		}
		if err := cursor.Decode(&row); err != nil {
			return fmt.Errorf("failed to decode usage input segment row: %w", err)
		}
		summary.addInputSegments(row.InputTokens, row.Provider, row.RawData)
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("error iterating usage input segment cursor: %w", err)
	}
	return nil
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *MongoDBReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	pipeline := bson.A{}
	matchFilters, err := mongoUsageMatchFilters(params)
	if err != nil {
		return nil, err
	}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	providerNameExpr := mongoUsageGroupedProviderNameExpr()
	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: bson.D{
			{Key: "model", Value: "$model"},
			{Key: "provider", Value: "$provider"},
			{Key: "provider_name", Value: providerNameExpr},
		}},
		{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
		{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
		{Key: "input_cost", Value: mongoCostSum("$input_cost")},
		{Key: "output_cost", Value: mongoCostSum("$output_cost")},
		{Key: "total_cost", Value: mongoCostSum("$total_cost")},
		{Key: "has_input_cost", Value: mongoCostPresenceCount("$input_cost")},
		{Key: "has_output_cost", Value: mongoCostPresenceCount("$output_cost")},
		{Key: "has_total_cost", Value: mongoCostPresenceCount("$total_cost")},
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
				Model        string `bson:"model"`
				Provider     string `bson:"provider"`
				ProviderName string `bson:"provider_name"`
			} `bson:"_id"`
			InputTokens   int64   `bson:"input_tokens"`
			OutputTokens  int64   `bson:"output_tokens"`
			InputCost     float64 `bson:"input_cost"`
			OutputCost    float64 `bson:"output_cost"`
			TotalCost     float64 `bson:"total_cost"`
			HasInputCost  int     `bson:"has_input_cost"`
			HasOutputCost int     `bson:"has_output_cost"`
			HasTotalCost  int     `bson:"has_total_cost"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode usage by model row: %w", err)
		}
		m := ModelUsage{
			Model:        row.ID.Model,
			Provider:     row.ID.Provider,
			ProviderName: displayUsageProviderName(row.ID.ProviderName, row.ID.Provider),
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
		}
		m.InputCost = costPtr(row.HasInputCost, row.InputCost)
		m.OutputCost = costPtr(row.HasOutputCost, row.OutputCost)
		m.TotalCost = costPtr(row.HasTotalCost, row.TotalCost)
		result = append(result, m)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by model cursor: %w", err)
	}

	return result, nil
}

// GetUsageByUserPath returns token and cost totals grouped by tracked user path.
func (r *MongoDBReader) GetUsageByUserPath(ctx context.Context, params UsageQueryParams) ([]UserPathUsage, error) {
	pipeline := bson.A{}
	matchParams := params
	matchParams.UserPath = ""
	matchFilters, err := mongoUsageMatchFilters(matchParams)
	if err != nil {
		return nil, err
	}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	const canonicalUserPathField = "_gomodel_user_path"
	pipeline = append(pipeline, bson.D{{Key: "$addFields", Value: bson.D{
		{Key: canonicalUserPathField, Value: mongoUsageGroupedUserPathExpr()},
	}}})

	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.D{{Key: canonicalUserPathField, Value: bson.D{
			{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
		}}}}})
	}

	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: "$" + canonicalUserPathField},
		{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
		{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
		{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
		{Key: "input_cost", Value: mongoCostSum("$input_cost")},
		{Key: "output_cost", Value: mongoCostSum("$output_cost")},
		{Key: "total_cost", Value: mongoCostSum("$total_cost")},
		{Key: "has_input_cost", Value: mongoCostPresenceCount("$input_cost")},
		{Key: "has_output_cost", Value: mongoCostPresenceCount("$output_cost")},
		{Key: "has_total_cost", Value: mongoCostPresenceCount("$total_cost")},
	}}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage by user path: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]UserPathUsage, 0)
	for cursor.Next(ctx) {
		var row struct {
			UserPath      string  `bson:"_id"`
			InputTokens   int64   `bson:"input_tokens"`
			OutputTokens  int64   `bson:"output_tokens"`
			TotalTokens   int64   `bson:"total_tokens"`
			InputCost     float64 `bson:"input_cost"`
			OutputCost    float64 `bson:"output_cost"`
			TotalCost     float64 `bson:"total_cost"`
			HasInputCost  int     `bson:"has_input_cost"`
			HasOutputCost int     `bson:"has_output_cost"`
			HasTotalCost  int     `bson:"has_total_cost"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode usage by user path row: %w", err)
		}
		u := UserPathUsage{
			UserPath:     row.UserPath,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
		}
		if u.UserPath == "" {
			u.UserPath = "/"
		}
		u.InputCost = costPtr(row.HasInputCost, row.InputCost)
		u.OutputCost = costPtr(row.HasOutputCost, row.OutputCost)
		u.TotalCost = costPtr(row.HasTotalCost, row.TotalCost)
		result = append(result, u)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by user path cursor: %w", err)
	}

	return result, nil
}

func mongoUsageGroupedProviderNameExpr() bson.D {
	trimmedProviderName := bson.D{{Key: "$trim", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$provider_name", ""}}}},
	}}}
	return bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$ne", Value: bson.A{trimmedProviderName, ""}}},
		trimmedProviderName,
		bson.D{{Key: "$trim", Value: bson.D{{Key: "input", Value: "$provider"}}}},
	}}}
}

func mongoUsageGroupedUserPathExpr() bson.D {
	trimmedUserPath := bson.D{{Key: "$trim", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$user_path", ""}}}},
	}}}
	return bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$ne", Value: bson.A{trimmedUserPath, ""}}},
		trimmedUserPath,
		"/",
	}}}
}

// GetUsageLog returns a paginated list of individual usage log entries.
func (r *MongoDBReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	matchFilters, err := mongoUsageLogMatchFilters(params)
	if err != nil {
		return nil, err
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
		Data  []mongoUsageLogRow `bson:"data"`
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
		entries = append(entries, row.toUsageLogEntry())
	}

	return &UsageLogResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// GetUsageByRequestIDs returns usage entries grouped by request ID.
func (r *MongoDBReader) GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error) {
	requestIDs = compactNonEmptyStrings(requestIDs)
	if len(requestIDs) == 0 {
		return map[string][]UsageLogEntry{}, nil
	}

	cursor, err := r.collection.Find(ctx,
		bson.D{{Key: "request_id", Value: bson.D{{Key: "$in", Value: requestIDs}}}},
		options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}, {Key: "_id", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by request IDs: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []mongoUsageLogRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode usage by request ID rows: %w", err)
	}

	grouped := make(map[string][]UsageLogEntry, len(requestIDs))
	for _, row := range rows {
		entry := row.toUsageLogEntry()
		grouped[entry.RequestID] = append(grouped[entry.RequestID], entry)
	}

	return grouped, nil
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
	matchFilters, err := mongoUsageMatchFilters(params)
	if err != nil {
		return nil, err
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
			{Key: "input_cost", Value: mongoCostSum("$input_cost")},
			{Key: "output_cost", Value: mongoCostSum("$output_cost")},
			{Key: "total_cost", Value: mongoCostSum("$total_cost")},
			{Key: "has_input_cost", Value: mongoCostPresenceCount("$input_cost")},
			{Key: "has_output_cost", Value: mongoCostPresenceCount("$output_cost")},
			{Key: "has_total_cost", Value: mongoCostPresenceCount("$total_cost")},
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
			Date          string  `bson:"_id"`
			Requests      int     `bson:"requests"`
			InputTokens   int64   `bson:"input_tokens"`
			OutputTokens  int64   `bson:"output_tokens"`
			TotalTokens   int64   `bson:"total_tokens"`
			InputCost     float64 `bson:"input_cost"`
			OutputCost    float64 `bson:"output_cost"`
			TotalCost     float64 `bson:"total_cost"`
			HasInputCost  int     `bson:"has_input_cost"`
			HasOutputCost int     `bson:"has_output_cost"`
			HasTotalCost  int     `bson:"has_total_cost"`
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
		d.InputCost = costPtr(row.HasInputCost, row.InputCost)
		d.OutputCost = costPtr(row.HasOutputCost, row.OutputCost)
		d.TotalCost = costPtr(row.HasTotalCost, row.TotalCost)
		result = append(result, d)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating daily usage cursor: %w", err)
	}

	// Second pass: project the same period key onto each document and fold the
	// prompt-cache split from raw_data in Go (the split is provider-specific and
	// cannot be computed in the aggregation). Using the same $dateToString keeps
	// the period keys aligned with the grouped result above.
	splitPipeline := bson.A{}
	if len(matchFilters) > 0 {
		splitPipeline = append(splitPipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}
	splitPipeline = append(splitPipeline, bson.D{{Key: "$project", Value: bson.D{
		{Key: "period", Value: bson.D{{Key: "$dateToString", Value: bson.D{
			{Key: "format", Value: dateFormat},
			{Key: "date", Value: "$timestamp"},
			{Key: "timezone", Value: usageTimeZone(params)},
		}}}},
		{Key: "cache_type", Value: 1},
		{Key: "input_tokens", Value: 1},
		{Key: "provider", Value: 1},
		{Key: "raw_data", Value: 1},
	}}})
	splitCursor, err := r.collection.Aggregate(ctx, splitPipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily input segments: %w", err)
	}
	defer splitCursor.Close(ctx)
	splits := map[string]periodInputSplit{}
	for splitCursor.Next(ctx) {
		var row struct {
			Period      string         `bson:"period"`
			CacheType   string         `bson:"cache_type"`
			InputTokens int            `bson:"input_tokens"`
			Provider    string         `bson:"provider"`
			RawData     map[string]any `bson:"raw_data"`
		}
		if err := splitCursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode daily input segment row: %w", err)
		}
		// Skip local-cache hits: they are not provider input.
		if isLocalCacheType(&row.CacheType) {
			continue
		}
		accumulatePeriodSplit(splits, row.Period, row.InputTokens, row.Provider, row.RawData)
	}
	if err := splitCursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating daily input segment cursor: %w", err)
	}
	applyDailyInputSplit(result, splits)

	return result, nil
}

// GetCacheOverview returns cached-only aggregates for the admin dashboard.
func (r *MongoDBReader) GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error) {
	params.CacheMode = CacheModeCached

	matchFilters, err := mongoUsageMatchFilters(params)
	if err != nil {
		return nil, err
	}

	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}

	pipeline := bson.A{}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}
	pipeline = append(pipeline, bson.D{{Key: "$facet", Value: bson.D{
		{Key: "summary", Value: bson.A{
			bson.D{{Key: "$group", Value: bson.D{
				{Key: "_id", Value: nil},
				{Key: "total_hits", Value: bson.D{{Key: "$sum", Value: 1}}},
				{Key: "exact_hits", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$cache_type", CacheTypeExact}}}, 1, 0}}}}}},
				{Key: "semantic_hits", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$cache_type", CacheTypeSemantic}}}, 1, 0}}}}}},
				{Key: "total_input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
				{Key: "total_output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
				{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
				{Key: "total_saved_cost", Value: mongoCostSum("$total_cost")},
				{Key: "has_saved_cost", Value: mongoCostPresenceCount("$total_cost")},
			}}},
		}},
		{Key: "daily", Value: bson.A{
			bson.D{{Key: "$group", Value: bson.D{
				{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
					{Key: "format", Value: mongoDateFormat(interval)},
					{Key: "date", Value: "$timestamp"},
					{Key: "timezone", Value: usageTimeZone(params)},
				}}}},
				{Key: "hits", Value: bson.D{{Key: "$sum", Value: 1}}},
				{Key: "exact_hits", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$cache_type", CacheTypeExact}}}, 1, 0}}}}}},
				{Key: "semantic_hits", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$cache_type", CacheTypeSemantic}}}, 1, 0}}}}}},
				{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
				{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
				{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
				{Key: "saved_cost", Value: mongoCostSum("$total_cost")},
				{Key: "has_saved_cost", Value: mongoCostPresenceCount("$total_cost")},
			}}},
			bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
		}},
	}}})

	overview := &CacheOverview{Daily: []CacheOverviewDaily{}}
	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate cache overview: %w", err)
	}
	defer cursor.Close(ctx)

	var facetResult struct {
		Summary []struct {
			TotalHits      int     `bson:"total_hits"`
			ExactHits      int     `bson:"exact_hits"`
			SemanticHits   int     `bson:"semantic_hits"`
			TotalInput     int64   `bson:"total_input_tokens"`
			TotalOutput    int64   `bson:"total_output_tokens"`
			TotalTokens    int64   `bson:"total_tokens"`
			TotalSavedCost float64 `bson:"total_saved_cost"`
			HasSavedCost   int     `bson:"has_saved_cost"`
		} `bson:"summary"`
		Daily []struct {
			Date         string  `bson:"_id"`
			Hits         int     `bson:"hits"`
			ExactHits    int     `bson:"exact_hits"`
			SemanticHits int     `bson:"semantic_hits"`
			InputTokens  int64   `bson:"input_tokens"`
			OutputTokens int64   `bson:"output_tokens"`
			TotalTokens  int64   `bson:"total_tokens"`
			SavedCost    float64 `bson:"saved_cost"`
			HasSavedCost int     `bson:"has_saved_cost"`
		} `bson:"daily"`
	}

	if cursor.Next(ctx) {
		if err := cursor.Decode(&facetResult); err != nil {
			return nil, fmt.Errorf("failed to decode cache overview facet result: %w", err)
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating cache overview cursor: %w", err)
	}

	if len(facetResult.Summary) > 0 {
		row := facetResult.Summary[0]
		overview.Summary = CacheOverviewSummary{
			TotalHits:    row.TotalHits,
			ExactHits:    row.ExactHits,
			SemanticHits: row.SemanticHits,
			TotalInput:   row.TotalInput,
			TotalOutput:  row.TotalOutput,
			TotalTokens:  row.TotalTokens,
		}
		if row.HasSavedCost > 0 {
			overview.Summary.TotalSavedCost = &row.TotalSavedCost
		}
	}

	for _, row := range facetResult.Daily {
		entry := CacheOverviewDaily{
			Date:         row.Date,
			Hits:         row.Hits,
			ExactHits:    row.ExactHits,
			SemanticHits: row.SemanticHits,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
		}
		entry.SavedCost = costPtr(row.HasSavedCost, row.SavedCost)
		overview.Daily = append(overview.Daily, entry)
	}

	return overview, nil
}

// GetTokenThroughput returns the trailing window of token-volume buckets for the
// overview live-throughput chart. Documents are streamed within the window and
// bucketed in Go (the prompt-cache split lives in raw_data), mirroring the
// summary input-segment pass. See foldThroughput in throughput.go for why this
// streams-and-folds rather than grouping in the database (and TODO(perf) there).
func (r *MongoDBReader) GetTokenThroughput(ctx context.Context, gran ThroughputGranularity, end time.Time, offset int64) (*TokenThroughput, error) {
	acc := newThroughputAccumulator(gran, end, offset)
	bucketSeconds, first, upper := throughputWindow(gran, end, offset)

	match := bson.D{{Key: "timestamp", Value: bson.D{
		{Key: "$gte", Value: time.Unix(first, 0).UTC()},
		{Key: "$lt", Value: time.Unix(upper, 0).UTC()},
	}}}
	projection := bson.D{
		{Key: "timestamp", Value: 1},
		{Key: "cache_type", Value: 1},
		{Key: "input_tokens", Value: 1},
		{Key: "output_tokens", Value: 1},
		{Key: "total_tokens", Value: 1},
		{Key: "provider", Value: 1},
		{Key: "raw_data", Value: 1},
	}

	cursor, err := r.collection.Find(ctx, match, options.Find().SetProjection(projection))
	if err != nil {
		return nil, fmt.Errorf("failed to query token throughput: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var row struct {
			Timestamp    time.Time      `bson:"timestamp"`
			CacheType    string         `bson:"cache_type"`
			InputTokens  int            `bson:"input_tokens"`
			OutputTokens int            `bson:"output_tokens"`
			TotalTokens  int            `bson:"total_tokens"`
			Provider     string         `bson:"provider"`
			RawData      map[string]any `bson:"raw_data"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode token throughput row: %w", err)
		}
		bucketStart := throughputBucketStart(row.Timestamp.Unix(), bucketSeconds, offset)
		acc.add(bucketStart, row.CacheType, row.Provider, row.InputTokens, row.OutputTokens, row.TotalTokens, row.RawData)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating token throughput cursor: %w", err)
	}
	return acc.result(gran), nil
}

func mongoUsageMatchFilters(params UsageQueryParams) (bson.D, error) {
	matchFilters := bson.D{}
	if tsFilter := mongoDateRangeFilter(params); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "user_path",
			Value: bson.D{
				{Key: "$regex", Value: usageUserPathSubtreeRegex(userPath)},
			},
		})
	}
	if filter := mongoCacheModeFilter(params.CacheMode); len(filter) > 0 {
		matchFilters = append(matchFilters, filter...)
	}
	return matchFilters, nil
}

func mongoUsageLogMatchFilters(params UsageLogParams) (bson.D, error) {
	matchFilters, err := mongoUsageMatchFilters(params.UsageQueryParams)
	if err != nil {
		return nil, err
	}

	if params.Model != "" {
		matchFilters = append(matchFilters, bson.E{Key: "model", Value: params.Model})
	}
	if params.Provider != "" {
		matchFilters = mongoAndFilters(matchFilters, bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "provider", Value: params.Provider}},
			bson.D{{Key: "provider_name", Value: params.Provider}},
		}}})
	}
	if params.Search != "" {
		regex := bson.D{{Key: "$regex", Value: regexp.QuoteMeta(params.Search)}, {Key: "$options", Value: "i"}}
		searchFilter := bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "model", Value: regex}},
			bson.D{{Key: "provider", Value: regex}},
			bson.D{{Key: "provider_name", Value: regex}},
			bson.D{{Key: "request_id", Value: regex}},
			bson.D{{Key: "provider_id", Value: regex}},
		}}}
		matchFilters = mongoAndFilters(matchFilters, searchFilter)
	}

	return matchFilters, nil
}

func mongoAndFilters(filters ...bson.D) bson.D {
	nonEmpty := make([]bson.D, 0, len(filters))
	for _, filter := range filters {
		if len(filter) > 0 {
			nonEmpty = append(nonEmpty, filter)
		}
	}

	switch len(nonEmpty) {
	case 0:
		return nil
	case 1:
		return nonEmpty[0]
	default:
		clauses := make(bson.A, 0, len(nonEmpty))
		for _, filter := range nonEmpty {
			clauses = append(clauses, filter)
		}
		return bson.D{{Key: "$and", Value: clauses}}
	}
}

func mongoCacheModeFilter(mode string) bson.D {
	switch normalizeCacheMode(mode) {
	case CacheModeCached:
		return bson.D{{Key: "cache_type", Value: bson.D{{Key: "$in", Value: bson.A{CacheTypeExact, CacheTypeSemantic}}}}}
	case CacheModeAll:
		return nil
	default:
		return bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "cache_type", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "cache_type", Value: nil}},
			bson.D{{Key: "cache_type", Value: ""}},
		}}}
	}
}
