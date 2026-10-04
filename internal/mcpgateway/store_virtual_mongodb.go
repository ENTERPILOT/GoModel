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

type mongoMCPVirtualDocument struct {
	ID            string    `bson:"_id"`
	Description   string    `bson:"description,omitempty"`
	Servers       []string  `bson:"servers"`
	ToolDiscovery string    `bson:"tool_discovery,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at"`
}

func (s *MongoDBStore) ListVirtual(ctx context.Context) ([]ManagedVirtualServer, error) {
	return listMongo(ctx, s.virtuals, "mcp virtual servers", managedVirtualFromMongo)
}

func (s *MongoDBStore) GetVirtual(ctx context.Context, name string) (*ManagedVirtualServer, error) {
	var doc mongoMCPVirtualDocument
	err := s.virtuals.FindOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get mcp virtual server: %w", err)
	}
	virtual := managedVirtualFromMongo(doc)
	return &virtual, nil
}

func (s *MongoDBStore) UpsertVirtual(ctx context.Context, virtual ManagedVirtualServer) error {
	stampVirtualUpsert(&virtual)
	update := bson.M{
		"$set":         mongoMCPVirtualFields(virtual),
		"$setOnInsert": bson.M{"created_at": virtual.CreatedAt},
	}
	_, err := s.virtuals.UpdateOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(virtual.Name)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert mcp virtual server: %w", err)
	}
	return nil
}

func (s *MongoDBStore) UpdateVirtual(ctx context.Context, virtual ManagedVirtualServer) error {
	stampVirtualUpsert(&virtual)
	result, err := s.virtuals.UpdateOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(virtual.Name)}, bson.M{"$set": mongoMCPVirtualFields(virtual)})
	if err != nil {
		return fmt.Errorf("update mcp virtual server: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) DeleteVirtual(ctx context.Context, name string) error {
	result, err := s.virtuals.DeleteOne(ctx, mongoMCPServerIDFilter{ID: strings.TrimSpace(name)})
	if err != nil {
		return fmt.Errorf("delete mcp virtual server: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func mongoMCPVirtualFields(virtual ManagedVirtualServer) bson.M {
	servers := virtual.Servers
	if servers == nil {
		servers = []string{}
	}
	return bson.M{
		"description":    virtual.Description,
		"servers":        servers,
		"tool_discovery": virtual.ToolDiscovery,
		"updated_at":     virtual.UpdatedAt,
	}
}

func managedVirtualFromMongo(doc mongoMCPVirtualDocument) ManagedVirtualServer {
	return ManagedVirtualServer{
		Name:          doc.ID,
		Description:   doc.Description,
		Servers:       doc.Servers,
		ToolDiscovery: doc.ToolDiscovery,
		CreatedAt:     doc.CreatedAt.UTC(),
		UpdatedAt:     doc.UpdatedAt.UTC(),
	}
}
