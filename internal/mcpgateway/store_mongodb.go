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

type mongoMCPServerDocument struct {
	ID                 string            `bson:"_id"`
	DisplayName        string            `bson:"display_name,omitempty"`
	URL                string            `bson:"url,omitempty"`
	Transport          string            `bson:"transport,omitempty"`
	Headers            map[string]string `bson:"headers,omitempty"`
	Description        string            `bson:"description,omitempty"`
	Enabled            bool              `bson:"enabled"`
	AllowedTools       []string          `bson:"allowed_tools,omitempty"`
	DisallowedTools    []string          `bson:"disallowed_tools,omitempty"`
	UserPaths          []string          `bson:"user_paths,omitempty"`
	ToolTimeoutSeconds int               `bson:"tool_timeout_seconds,omitempty"`
	CreatedAt          time.Time         `bson:"created_at"`
	UpdatedAt          time.Time         `bson:"updated_at"`
}

type mongoMCPServerIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores managed MCP servers in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("mcp_servers")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create mcp_servers indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]ManagedServer, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]ManagedServer, 0)
	for cursor.Next(ctx) {
		var doc mongoMCPServerDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode mcp server: %w", err)
		}
		result = append(result, managedServerFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp servers: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, name string) (*ManagedServer, error) {
	var doc mongoMCPServerDocument
	err := s.collection.FindOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get mcp server: %w", err)
	}
	server := managedServerFromMongo(doc)
	return &server, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, server ManagedServer) error {
	stampUpsert(&server)
	update := bson.M{
		"$set": bson.M{
			"display_name":         server.DisplayName,
			"url":                  server.URL,
			"transport":            server.Transport,
			"headers":              server.Headers,
			"description":          server.Description,
			"enabled":              server.Enabled,
			"allowed_tools":        server.AllowedTools,
			"disallowed_tools":     server.DisallowedTools,
			"user_paths":           server.UserPaths,
			"tool_timeout_seconds": server.ToolTimeoutSeconds,
			"updated_at":           server.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": server.CreatedAt,
		},
	}
	_, err := s.collection.UpdateOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(server.Name)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert mcp server: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)})
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func managedServerFromMongo(doc mongoMCPServerDocument) ManagedServer {
	server := ManagedServer{
		Name:               doc.ID,
		DisplayName:        doc.DisplayName,
		URL:                doc.URL,
		Transport:          doc.Transport,
		Description:        doc.Description,
		Enabled:            doc.Enabled,
		ToolTimeoutSeconds: doc.ToolTimeoutSeconds,
		CreatedAt:          doc.CreatedAt.UTC(),
		UpdatedAt:          doc.UpdatedAt.UTC(),
	}
	if server.DisplayName == "" {
		server.DisplayName = server.Name
	}
	if len(doc.Headers) > 0 {
		server.Headers = doc.Headers
	}
	if len(doc.AllowedTools) > 0 {
		server.AllowedTools = append([]string(nil), doc.AllowedTools...)
	}
	if len(doc.DisallowedTools) > 0 {
		server.DisallowedTools = append([]string(nil), doc.DisallowedTools...)
	}
	if len(doc.UserPaths) > 0 {
		server.UserPaths = append([]string(nil), doc.UserPaths...)
	}
	return server
}
