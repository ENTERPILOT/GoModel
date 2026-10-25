package usage

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// GetUsageBreakdown returns a bounded two-dimension pivot of the filtered
// usage. One aggregation groups every (row, column) pair; the top-N cut and
// the "other" bucket are folded in Go, shared with the SQL backends.
func (r *MongoDBReader) GetUsageBreakdown(ctx context.Context, params UsageBreakdownParams) (*UsageBreakdown, error) {
	params, err := NormalizeUsageBreakdownParams(params)
	if err != nil {
		return nil, err
	}
	pipeline, err := mongoUsageBreakdownPipeline(params)
	if err != nil {
		return nil, err
	}

	cursor, err := r.collection.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage breakdown: %w", err)
	}
	defer cursor.Close(ctx)

	groups := make([]breakdownGroup, 0)
	for cursor.Next(ctx) {
		var row struct {
			ID struct {
				Row    string `bson:"row"`
				Column string `bson:"column"`
			} `bson:"_id"`
			Requests      int     `bson:"requests"`
			InputTokens   int64   `bson:"input_tokens"`
			OutputTokens  int64   `bson:"output_tokens"`
			TotalTokens   int64   `bson:"total_tokens"`
			TotalCost     float64 `bson:"total_cost"`
			HasTotalCost  int     `bson:"has_total_cost"`
			BilledCost    float64 `bson:"billed_cost"`
			HasBilledCost int     `bson:"has_billed_cost"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("failed to decode usage breakdown row: %w", err)
		}
		groups = append(groups, breakdownGroup{
			Row: row.ID.Row, Column: row.ID.Column, Requests: row.Requests,
			InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens,
			TotalCost:  costPtr(row.HasTotalCost, row.TotalCost),
			BilledCost: costPtr(row.HasBilledCost, row.BilledCost),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage breakdown cursor: %w", err)
	}
	if len(groups) > maxBreakdownGroups {
		return nil, tooManyBreakdownGroupsError()
	}
	return buildUsageBreakdown(groups, params), nil
}

// mongoUsageBreakdownPipeline builds the grouping aggregation. Breaking down
// by user path filters on the canonical path it groups by; breaking down by
// label unwinds the labels while keeping unlabelled documents.
func mongoUsageBreakdownPipeline(params UsageBreakdownParams) (bson.A, error) {
	byUserPath := params.usesDimension(BreakdownUserPath)
	matchParams := params.UsageQueryParams
	if byUserPath {
		matchParams = withoutUserPathFilters(matchParams)
	}
	matchFilters, err := mongoUsageMatchFilters(matchParams)
	if err != nil {
		return nil, err
	}
	pipeline := bson.A{}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}
	if byUserPath {
		pipeline = append(pipeline, mongoCanonicalUserPathAddFieldsStage())
		matchStage, err := mongoCanonicalUserPathMatchStage(params.UsageQueryParams)
		if err != nil {
			return nil, err
		}
		if matchStage != nil {
			pipeline = append(pipeline, matchStage)
		}
	}
	if params.usesDimension(BreakdownLabel) {
		pipeline = append(pipeline, bson.D{{Key: "$unwind", Value: bson.D{
			{Key: "path", Value: "$labels"},
			{Key: "preserveNullAndEmptyArrays", Value: true},
		}}})
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "row", Value: mongoBreakdownKeyExpr(params, params.Rows)},
				{Key: "column", Value: mongoBreakdownKeyExpr(params, params.Columns)},
			}},
			{Key: "requests", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "input_tokens", Value: bson.D{{Key: "$sum", Value: "$input_tokens"}}},
			{Key: "output_tokens", Value: bson.D{{Key: "$sum", Value: "$output_tokens"}}},
			{Key: "total_tokens", Value: bson.D{{Key: "$sum", Value: "$total_tokens"}}},
			{Key: "total_cost", Value: mongoCostSum("$total_cost")},
			{Key: "has_total_cost", Value: mongoCostPresenceCount("$total_cost")},
			{Key: "billed_cost", Value: mongoCostSum("$billed_cost")},
			{Key: "has_billed_cost", Value: mongoCostPresenceCount("$billed_cost")},
		}}},
		bson.D{{Key: "$limit", Value: maxBreakdownGroups + 1}},
	)
	return pipeline, nil
}

// mongoBreakdownKeyExpr returns the expression yielding dim's key for each
// document (never null).
func mongoBreakdownKeyExpr(params UsageBreakdownParams, dim BreakdownDimension) any {
	ifNull := func(field string) bson.D {
		return bson.D{{Key: "$ifNull", Value: bson.A{field, ""}}}
	}
	switch dim {
	case BreakdownModel:
		return ifNull("$model")
	case BreakdownProvider:
		return mongoUsageGroupedProviderNameExpr()
	case BreakdownUserPath:
		return "$" + mongoCanonicalUserPathField
	case BreakdownLabel:
		return ifNull("$labels")
	case BreakdownEndpoint:
		return ifNull("$endpoint")
	case BreakdownAuthKey:
		return ifNull("$auth_key_id")
	case BreakdownPeriod:
		interval := params.Interval
		if interval == "" {
			interval = "daily"
		}
		return bson.D{{Key: "$dateToString", Value: bson.D{
			{Key: "format", Value: mongoDateFormat(interval)},
			{Key: "date", Value: "$timestamp"},
			{Key: "timezone", Value: usageTimeZone(params.UsageQueryParams)},
		}}}
	default:
		return bson.D{{Key: "$literal", Value: ""}}
	}
}
