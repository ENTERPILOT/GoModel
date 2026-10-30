package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoOAuthSwapFilter matches a record by name and stored value. Typed
// filters keep request values out of operator positions.
type mongoOAuthSwapFilter struct {
	ID   string `bson:"_id"`
	Data string `bson:"data"`
}

type mongoOAuthDocument struct {
	ID        string    `bson:"_id"`
	Data      string    `bson:"data"`
	UpdatedAt time.Time `bson:"updated_at"`
}

func (s *MongoDBStore) listOAuth(ctx context.Context) (map[string]string, error) {
	docs, err := listMongo(ctx, s.oauth, "mcp oauth records", func(doc mongoOAuthDocument) mongoOAuthDocument { return doc })
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(docs))
	for _, doc := range docs {
		result[doc.ID] = doc.Data
	}
	return result, nil
}

func (s *MongoDBStore) getOAuth(ctx context.Context, name string) (string, error) {
	var doc mongoOAuthDocument
	err := s.oauth.FindOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get mcp oauth record: %w", err)
	}
	return doc.Data, nil
}

func (s *MongoDBStore) putOAuth(ctx context.Context, name, data string) error {
	update := bson.M{"$set": bson.M{"data": data, "updated_at": time.Now().UTC()}}
	_, err := s.oauth.UpdateOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("save mcp oauth record: %w", err)
	}
	return nil
}

func (s *MongoDBStore) deleteOAuth(ctx context.Context, name string) error {
	if _, err := s.oauth.DeleteOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)}); err != nil {
		return fmt.Errorf("delete mcp oauth record: %w", err)
	}
	return nil
}

func (s *MongoDBStore) swapOAuth(ctx context.Context, name, current, next string) (bool, error) {
	result, err := s.oauth.UpdateOne(ctx,
		mongoOAuthSwapFilter{ID: strings.TrimSpace(name), Data: current},
		bson.M{"$set": bson.M{"data": next}})
	if err != nil {
		return false, fmt.Errorf("swap mcp oauth record: %w", err)
	}
	return result.MatchedCount > 0, nil
}
