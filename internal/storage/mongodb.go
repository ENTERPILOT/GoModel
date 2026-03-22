package storage

import (
	"context"
	"database/sql"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoStorage implements Storage for MongoDB
type mongoStorage struct {
	client   *mongo.Client
	database *mongo.Database
}

// NewMongoDB creates a new MongoDB storage connection.
func NewMongoDB(ctx context.Context, cfg MongoDBConfig) (Storage, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("MongoDB URL is required")
	}

	// Set default database name
	dbName := cfg.Database
	if dbName == "" {
		dbName = "gomodel"
	}

	// Create client options
	clientOpts := options.Client().ApplyURI(cfg.URL)

	// Connect to MongoDB
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Verify connection
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	// Get database reference
	database := client.Database(dbName)

	return &mongoStorage{
		client:   client,
		database: database,
	}, nil
}

func (s *mongoStorage) Type() string {
	return TypeMongoDB
}

func (s *mongoStorage) SQLiteDB() *sql.DB {
	return nil
}

func (s *mongoStorage) PostgreSQLPool() any {
	return nil
}

func (s *mongoStorage) MongoDatabase() any {
	return s.database
}

func (s *mongoStorage) Close() error {
	if s.client != nil {
		return s.client.Disconnect(context.Background())
	}
	return nil
}

// Database returns the underlying *mongo.Database for direct access
func (s *mongoStorage) Database() *mongo.Database {
	return s.database
}

// Client returns the underlying *mongo.Client for direct access
func (s *mongoStorage) Client() *mongo.Client {
	return s.client
}
