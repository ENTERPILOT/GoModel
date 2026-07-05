package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type mongoPricingSession interface {
	WithTransaction(context.Context, func(context.Context) (any, error)) (any, error)
	EndSession(context.Context)
}

type mongoDriverPricingSession struct {
	session *mongo.Session
}

func (s mongoDriverPricingSession) WithTransaction(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	return s.session.WithTransaction(ctx, fn)
}

func (s mongoDriverPricingSession) EndSession(ctx context.Context) {
	s.session.EndSession(ctx)
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
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		next, err := s.recalculatePricingDocumentsInContext(txCtx, filter, resolver)
		if err != nil {
			if isMongoTransactionCapabilityError(err) {
				return nil, &mongoTransactionFallbackError{err: err}
			}
			return nil, err
		}
		result = next
		return nil, nil
	})
	if err != nil {
		if fallbackErr := mongoTransactionFallbackCause(err); fallbackErr != nil || isMongoTransactionCapabilityError(err) {
			if fallbackErr == nil {
				fallbackErr = err
			}
			slog.Warn("MongoDB transactions unavailable for pricing recalculation; falling back to non-transactional update", "error", fallbackErr)
			result, err := s.recalculatePricingDocumentsInContext(ctx, filter, resolver)
			if err != nil {
				return RecalculatePricingResult{}, fmt.Errorf("recalculate mongodb usage costs without transaction: %w", errors.Join(fallbackErr, err))
			}
			return finalizeRecalculatePricingResult(result), nil
		}
		return RecalculatePricingResult{}, fmt.Errorf("mongodb pricing recalculation transaction: %w", err)
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
	return mongoDriverPricingSession{session: session}, nil
}

func (s *MongoDBStore) recalculatePricingDocumentsInContext(ctx context.Context, filter bson.D, resolver PricingResolver) (RecalculatePricingResult, error) {
	if s.recalculatePricingDocuments != nil {
		return s.recalculatePricingDocuments(ctx, filter, resolver)
	}
	return s.recalculatePricingInMongoTransaction(ctx, filter, resolver)
}

type mongoTransactionFallbackError struct {
	err error
}

func (e *mongoTransactionFallbackError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func mongoTransactionFallbackCause(err error) error {
	var fallbackErr *mongoTransactionFallbackError
	if errors.As(err, &fallbackErr) {
		return fallbackErr.err
	}
	return nil
}

func isMongoTransactionCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	var commandErr mongo.CommandError
	if errors.As(err, &commandErr) && commandErr.HasErrorCode(20) {
		return true
	}
	var labeled mongo.LabeledError
	if errors.As(err, &labeled) && labeled.HasErrorLabel("TransientTransactionError") {
		message := strings.ToLower(err.Error())
		return strings.Contains(message, "transaction") &&
			(strings.Contains(message, "not supported") ||
				strings.Contains(message, "not allowed") ||
				strings.Contains(message, "replica set"))
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "transaction numbers are only allowed on a replica set member or mongos")
}

func (s *MongoDBStore) recalculatePricingInMongoTransaction(ctx context.Context, filter bson.D, resolver PricingResolver) (RecalculatePricingResult, error) {
	cursor, err := s.collection.Find(ctx, filter)
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("query mongodb usage costs for recalculation: %w", err)
	}
	defer cursor.Close(ctx)

	result := RecalculatePricingResult{}
	for cursor.Next(ctx) {
		var row struct {
			ID                 string         `bson:"_id"`
			Model              string         `bson:"model"`
			Provider           string         `bson:"provider"`
			ProviderName       string         `bson:"provider_name"`
			Endpoint           string         `bson:"endpoint"`
			InputTokens        int            `bson:"input_tokens"`
			OutputTokens       int            `bson:"output_tokens"`
			RewriteTokensSaved int            `bson:"rewrite_tokens_saved"`
			RawData            map[string]any `bson:"raw_data"`
		}
		if err := cursor.Decode(&row); err != nil {
			return RecalculatePricingResult{}, fmt.Errorf("scan mongodb usage cost row: %w", err)
		}

		update := recalculateEntryCosts(recalculationEntry{
			ID:                 row.ID,
			Model:              row.Model,
			Provider:           row.Provider,
			ProviderName:       row.ProviderName,
			Endpoint:           row.Endpoint,
			InputTokens:        row.InputTokens,
			OutputTokens:       row.OutputTokens,
			RewriteTokensSaved: row.RewriteTokensSaved,
			RawData:            row.RawData,
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
