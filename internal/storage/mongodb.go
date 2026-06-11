package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DefaultMongoDatabase is the database used when neither the explicit Database
// config nor the connection string names one.
const DefaultMongoDatabase = "gomodel"

// mongoStorage implements Storage for MongoDB
type mongoStorage struct {
	client   *mongo.Client
	database *mongo.Database
}

// NewMongoDB creates a new MongoDB storage connection.
func NewMongoDB(ctx context.Context, cfg MongoDBConfig) (MongoDBStorage, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("MongoDB URL is required")
	}

	dbName := resolveMongoDatabase(cfg)

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

// resolveMongoDatabase picks the database name for a connection: the explicit
// Database config wins, then the database named in the connection string path
// (the standard "mongodb://host/dbname" form), then DefaultMongoDatabase.
func resolveMongoDatabase(cfg MongoDBConfig) string {
	if cfg.Database != "" {
		return cfg.Database
	}
	if name := databaseNameFromURI(cfg.URL); name != "" {
		return name
	}
	return DefaultMongoDatabase
}

// databaseNameFromURI extracts the database name (the path component) from a
// MongoDB connection string. Returns "" when the URI is unparseable, names no
// database, or the path is not a single segment — MongoDB database names
// cannot contain '/', so a multi-segment path would fail at connect time.
func databaseNameFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	name := strings.Trim(u.Path, "/")
	if strings.Contains(name, "/") {
		return ""
	}
	return name
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
