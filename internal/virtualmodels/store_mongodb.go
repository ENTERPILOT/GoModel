package virtualmodels

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

type mongoVirtualModelDocument struct {
	ID              string    `bson:"_id"`
	Targets         []Target  `bson:"targets,omitempty"`
	Strategy        string    `bson:"strategy,omitempty"`
	StrategyPlugin  string    `bson:"strategy_plugin,omitempty"`
	StrategyConfig  bson.M    `bson:"strategy_config,omitempty"`
	SessionAffinity *bool     `bson:"session_affinity,omitempty"`
	Failover        *bool     `bson:"failover,omitempty"`
	ProviderName    string    `bson:"provider_name,omitempty"`
	Model           string    `bson:"model,omitempty"`
	UserPaths       []string  `bson:"user_paths,omitempty"`
	Description     string    `bson:"description,omitempty"`
	Slowdown        *float64  `bson:"slowdown,omitempty"`
	Enabled         bool      `bson:"enabled"`
	CreatedAt       time.Time `bson:"created_at"`
	UpdatedAt       time.Time `bson:"updated_at"`
}

type mongoVirtualModelIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores virtual models in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("virtual_models")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "provider_name", Value: 1}}},
		{Keys: bson.D{{Key: "model", Value: 1}}},
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create virtual_models indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]VirtualModel, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list virtual models: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]VirtualModel, 0)
	for cursor.Next(ctx) {
		var doc mongoVirtualModelDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode virtual model: %w", err)
		}
		result = append(result, virtualModelFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate virtual models: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, source string) (*VirtualModel, error) {
	var doc mongoVirtualModelDocument
	err := s.collection.FindOne(ctx, mongoVirtualModelIDFilter{ID: strings.TrimSpace(source)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get virtual model: %w", err)
	}
	vm := virtualModelFromMongo(doc)
	return &vm, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, vm VirtualModel) error {
	stampUpsert(&vm)
	update := bson.M{
		"$set": bson.M{
			"targets":          vm.Targets,
			"strategy":         vm.Strategy,
			"strategy_plugin":  vm.StrategyPlugin,
			"strategy_config":  vm.StrategyConfig,
			"session_affinity": vm.SessionAffinity,
			"failover":         vm.Failover,
			"provider_name":    vm.ProviderName,
			"model":            vm.Model,
			"user_paths":       vm.UserPaths,
			"description":      vm.Description,
			"slowdown":         vm.Slowdown,
			"enabled":          vm.Enabled,
			"updated_at":       vm.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": vm.CreatedAt,
		},
	}
	_, err := s.collection.UpdateOne(ctx, mongoVirtualModelIDFilter{ID: strings.TrimSpace(vm.Source)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert virtual model: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, source string) error {
	result, err := s.collection.DeleteOne(ctx, mongoVirtualModelIDFilter{ID: strings.TrimSpace(source)})
	if err != nil {
		return fmt.Errorf("delete virtual model: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func virtualModelFromMongo(doc mongoVirtualModelDocument) VirtualModel {
	vm := VirtualModel{
		Source:          doc.ID,
		Strategy:        doc.Strategy,
		StrategyPlugin:  doc.StrategyPlugin,
		StrategyConfig:  strategyConfigFromBSON(doc.StrategyConfig),
		SessionAffinity: doc.SessionAffinity,
		Failover:        doc.Failover,
		ProviderName:    doc.ProviderName,
		Model:           doc.Model,
		Description:     doc.Description,
		Slowdown:        doc.Slowdown,
		Enabled:         doc.Enabled,
		CreatedAt:       doc.CreatedAt.UTC(),
		UpdatedAt:       doc.UpdatedAt.UTC(),
	}
	if len(doc.Targets) > 0 {
		vm.Targets = append([]Target(nil), doc.Targets...)
	}
	if len(doc.UserPaths) > 0 {
		vm.UserPaths = append([]string(nil), doc.UserPaths...)
	}
	return vm
}

// strategyConfigFromBSON turns a decoded config into plain maps and slices:
// the driver decodes nested documents as bson.D and arrays as bson.A, which
// plugin config validation does not understand.
func strategyConfigFromBSON(doc bson.M) map[string]any {
	if len(doc) == 0 {
		return nil
	}
	config, _ := plainBSONValue(doc).(map[string]any)
	return config
}

func plainBSONValue(value any) any {
	switch v := value.(type) {
	case bson.M:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = plainBSONValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = plainBSONValue(item)
		}
		return out
	case bson.D:
		out := make(map[string]any, len(v))
		for _, item := range v {
			out[item.Key] = plainBSONValue(item.Value)
		}
		return out
	case bson.A:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = plainBSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = plainBSONValue(item)
		}
		return out
	case int32:
		return int64(v)
	default:
		return v
	}
}
