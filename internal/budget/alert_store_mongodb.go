package budget

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// createAlertIndexes creates the unique index that makes an alert fire once
// across every instance on the database: the insert that creates the document
// wins.
func (s *MongoDBStore) createAlertIndexes(ctx context.Context) error {
	_, err := s.alerts.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "scope", Value: 1}, {Key: "subject", Value: 1}, {Key: "effective_subject", Value: 1},
				{Key: "budget_revision", Value: 1}, {Key: "period_seconds", Value: 1}, {Key: "period_start", Value: 1}, {Key: "threshold_percent", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "period_end", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("create budget alert indexes: %w", err)
	}
	return nil
}

func (s *MongoDBStore) ClaimAlert(ctx context.Context, claim AlertClaim) (bool, error) {
	_, err := s.alerts.InsertOne(ctx, bson.D{
		{Key: "scope", Value: claim.Scope},
		{Key: "subject", Value: claim.Subject},
		{Key: "effective_subject", Value: claim.EffectiveSubject},
		{Key: "budget_revision", Value: claim.Revision},
		{Key: "period_seconds", Value: claim.PeriodSeconds},
		{Key: "period_start", Value: claim.PeriodStart.Unix()},
		{Key: "threshold_percent", Value: claim.Threshold},
		{Key: "period_end", Value: claim.PeriodEnd.Unix()},
		{Key: "fired_at", Value: time.Now().UTC()},
	})
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim budget alert: %w", err)
	}
	return true, nil
}

func (s *MongoDBStore) PruneAlerts(ctx context.Context, cutoff time.Time) error {
	_, err := s.alerts.DeleteMany(ctx, bson.D{{Key: "period_end", Value: bson.D{{Key: "$lt", Value: cutoff.Unix()}}}})
	if err != nil {
		return fmt.Errorf("prune budget alerts: %w", err)
	}
	return nil
}

// SumSpendByChild groups the usage of one user-path window by the direct
// child of its subject in a single aggregation, matching the SQL stores.
func (s *MongoDBStore) SumSpendByChild(ctx context.Context, window SpendWindow, limit int) ([]ChildSpend, error) {
	if window.Scope != ScopeUserPath {
		return nil, fmt.Errorf("spend by child needs a user_path window, got %q", window.Scope)
	}
	subject, err := NormalizeUserPath(window.Subject)
	if err != nil {
		return nil, err
	}
	subjectMatch, err := mongoSubjectMatch(window)
	if err != nil {
		return nil, err
	}
	prefix := childPrefix(subject)
	path := mongoTrimmedOr("$user_path", "/")
	// The first segment of the path below the subject.
	segment := bson.D{{Key: "$let", Value: bson.D{
		{Key: "vars", Value: bson.D{{Key: "path", Value: path}}},
		{Key: "in", Value: bson.D{{Key: "$arrayElemAt", Value: bson.A{
			bson.D{{Key: "$split", Value: bson.A{
				bson.D{{Key: "$substrCP", Value: bson.A{
					"$$path", utf8.RuneCountInString(prefix),
					bson.D{{Key: "$strLenCP", Value: "$$path"}},
				}}},
				"/",
			}}},
			0,
		}}}},
	}}}
	cost := mongoSpendCost(window)
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "timestamp", Value: bson.D{{Key: "$gte", Value: window.Start.UTC()}, {Key: "$lt", Value: window.End.UTC()}}},
			// Usage on the subject itself is nobody's child.
			{Key: "$expr", Value: bson.D{{Key: "$and", Value: bson.A{
				subjectMatch,
				bson.D{{Key: "$ne", Value: bson.A{path, bson.D{{Key: "$literal", Value: subject}}}}},
			}}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: segment},
			{Key: "total", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{cost, 0}}}}}},
			{Key: "priced", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{cost, nil}}}, 1, 0,
			}}}}}},
		}}},
		// A child without priced usage has no spend, as in SumSpend.
		bson.D{{Key: "$match", Value: bson.D{{Key: "priced", Value: bson.D{{Key: "$gt", Value: 0}}}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "total", Value: -1}, {Key: "_id", Value: 1}}}},
		bson.D{{Key: "$limit", Value: limit}},
	}
	cursor, err := s.usage.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("sum usage cost by child: %w", err)
	}
	defer cursor.Close(ctx)

	var spends []ChildSpend
	for cursor.Next(ctx) {
		var row bson.M
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("decode usage cost by child: %w", err)
		}
		segment, _ := row["_id"].(string)
		total, _ := bsonNumber(row["total"])
		spends = append(spends, ChildSpend{Child: prefix + segment, Total: total})
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage cost by child: %w", err)
	}
	return spends, nil
}
