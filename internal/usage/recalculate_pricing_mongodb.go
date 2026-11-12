package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/enterpilot/gomodel/internal/storage"
)

type mongoPricingSession interface {
	storage.MongoTransactor
	EndSession(context.Context)
}

// RecalculatePricing updates matching MongoDB usage documents with costs
// computed from the supplied pricing resolver.
func (s *MongoDBStore) RecalculatePricing(ctx context.Context, params RecalculatePricingParams, resolver PricingResolver) (RecalculatePricingResult, error) {
	if err := recalculatePricingUnavailable(resolver); err != nil {
		return RecalculatePricingResult{}, err
	}
	params = normalizedRecalculatePricingParams(params)

	filter, err := mongoRecalculationFilter(params)
	if err != nil {
		return RecalculatePricingResult{}, err
	}

	session, err := s.startMongoPricingSession()
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("start mongodb pricing recalculation transaction: %w", err)
	}
	defer session.EndSession(ctx)

	var result RecalculatePricingResult
	err = storage.MongoTransaction(ctx, session, "mongodb pricing recalculation", func(ctx context.Context) error {
		next, err := s.recalculatePricingDocumentsInContext(ctx, filter, resolver, params.Billing)
		result = next
		return err
	})
	if err != nil {
		return RecalculatePricingResult{}, err
	}
	return finalizeRecalculatePricingResult(result), nil
}

func (s *MongoDBStore) startMongoPricingSession() (mongoPricingSession, error) {
	if s.startPricingSession != nil {
		return s.startPricingSession()
	}
	if s.collection == nil {
		return nil, fmt.Errorf("mongodb usage collection is not configured")
	}
	session, err := s.collection.Database().Client().StartSession()
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (s *MongoDBStore) recalculatePricingDocumentsInContext(ctx context.Context, filter bson.D, resolver PricingResolver, billing Billing) (RecalculatePricingResult, error) {
	if s.recalculatePricingDocuments != nil {
		return s.recalculatePricingDocuments(ctx, filter, resolver, billing)
	}
	return s.recalculatePricingInMongoTransaction(ctx, filter, resolver, billing)
}

func (s *MongoDBStore) recalculatePricingInMongoTransaction(ctx context.Context, filter bson.D, resolver PricingResolver, billing Billing) (RecalculatePricingResult, error) {
	cursor, err := s.collection.Find(ctx, filter)
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("query mongodb usage costs for recalculation: %w", err)
	}
	defer cursor.Close(ctx)

	result := RecalculatePricingResult{}
	for cursor.Next(ctx) {
		var row struct {
			ID                 string         `bson:"_id"`
			Timestamp          time.Time      `bson:"timestamp"`
			Model              string         `bson:"model"`
			Provider           string         `bson:"provider"`
			ProviderName       string         `bson:"provider_name"`
			Endpoint           string         `bson:"endpoint"`
			InputTokens        int            `bson:"input_tokens"`
			OutputTokens       int            `bson:"output_tokens"`
			RewriteTokensSaved int            `bson:"rewrite_tokens_saved"`
			RawData            map[string]any `bson:"raw_data"`
			Caveat             string         `bson:"costs_calculation_caveat"`
			CacheType          string         `bson:"cache_type"`
			MarkupPercent      *float64       `bson:"markup_percent"`
			Billable           *bool          `bson:"billable"`
		}
		if err := cursor.Decode(&row); err != nil {
			return RecalculatePricingResult{}, fmt.Errorf("scan mongodb usage cost row: %w", err)
		}

		update := recalculateEntryCosts(recalculationEntry{
			ID:                 row.ID,
			Timestamp:          row.Timestamp,
			Model:              row.Model,
			Provider:           row.Provider,
			ProviderName:       row.ProviderName,
			Endpoint:           row.Endpoint,
			InputTokens:        row.InputTokens,
			OutputTokens:       row.OutputTokens,
			RewriteTokensSaved: row.RewriteTokensSaved,
			RawData:            row.RawData,
			Caveat:             row.Caveat,
			CacheType:          row.CacheType,
			MarkupPercent:      row.MarkupPercent,
			NonBillable:        row.Billable != nil && !*row.Billable,
			Billing:            billing,
		}, resolver)

		if _, err := s.collection.UpdateByID(ctx, update.ID, mongoRecalculationUpdate(update)); err != nil {
			return RecalculatePricingResult{}, fmt.Errorf("update mongodb usage cost %s: %w", update.ID, err)
		}
		updateRecalculatePricingResult(&result, update)
	}
	if err := cursor.Err(); err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("iterate mongodb usage costs for recalculation: %w", err)
	}
	return finalizeRecalculatePricingResult(result), nil
}

func mongoRecalculationFilter(params RecalculatePricingParams) (bson.D, error) {
	return mongoUsageMatchFilters(params.UsageQueryParams)
}

func mongoRecalculationUpdate(update recalculationUpdate) bson.D {
	set := bson.D{{Key: "costs_calculation_caveat", Value: update.Caveat}}
	unset := bson.D{}

	if update.InputCost != nil {
		set = append(set, bson.E{Key: "input_cost", Value: *update.InputCost})
	} else {
		unset = append(unset, bson.E{Key: "input_cost", Value: ""})
	}
	if update.OutputCost != nil {
		set = append(set, bson.E{Key: "output_cost", Value: *update.OutputCost})
	} else {
		unset = append(unset, bson.E{Key: "output_cost", Value: ""})
	}
	if update.TotalCost != nil {
		set = append(set, bson.E{Key: "total_cost", Value: *update.TotalCost})
	} else {
		unset = append(unset, bson.E{Key: "total_cost", Value: ""})
	}
	if update.RewriteCostSaved != nil {
		set = append(set, bson.E{Key: "rewrite_cost_saved", Value: *update.RewriteCostSaved})
	} else {
		unset = append(unset, bson.E{Key: "rewrite_cost_saved", Value: ""})
	}
	switch {
	case update.KeepBilledCost:
	case update.BilledCost != nil:
		set = append(set, bson.E{Key: "billed_cost", Value: *update.BilledCost})
	default:
		unset = append(unset, bson.E{Key: "billed_cost", Value: ""})
	}
	if strings.TrimSpace(update.CostSource) != "" {
		set = append(set, bson.E{Key: "cost_source", Value: strings.TrimSpace(update.CostSource)})
	} else {
		unset = append(unset, bson.E{Key: "cost_source", Value: ""})
	}

	result := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		result = append(result, bson.E{Key: "$unset", Value: unset})
	}
	return result
}
