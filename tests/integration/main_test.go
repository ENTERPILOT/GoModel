//go:build integration

// Package integration provides integration tests that verify database state
// after HTTP requests. Tests run against real PostgreSQL and MongoDB instances
// using testcontainers-go.
package integration

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	// PostgreSQL resources
	pgContainer *postgres.PostgresContainer
	pgPool      *pgxpool.Pool
	pgURL       string

	// MongoDB resources
	mongoContainer *mongodb.MongoDBContainer
	mongoClient    *mongo.Client
	mongoDatabase  *mongo.Database
	mongoURL       string

	// Test context
	testCtx    context.Context
	cancelFunc context.CancelFunc
)

// TestMain sets up and tears down the test containers.
func TestMain(m *testing.M) {
	testCtx, cancelFunc = context.WithTimeout(context.Background(), 10*time.Minute)

	// Start containers in parallel
	errCh := make(chan error, 2)

	go func() {
		errCh <- setupPostgreSQL(testCtx)
	}()

	go func() {
		errCh <- setupMongoDB(testCtx)
	}()

	// Wait for both containers to start
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			log.Printf("Container setup failed: %v", err)
			cleanup()
			cancelFunc()
			os.Exit(1)
		}
	}

	log.Println("All containers started successfully")

	// Run tests
	code := m.Run()

	// Cleanup
	cleanup()
	cancelFunc()
	os.Exit(code)
}

// setupPostgreSQL starts a PostgreSQL container and creates the connection pool.
func setupPostgreSQL(ctx context.Context) error {
	var err error

	log.Println("Starting PostgreSQL container...")
	pgContainer, err = postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("gomodel_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to start PostgreSQL container: %w", err)
	}

	// Get connection string
	pgURL, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("failed to get PostgreSQL connection string: %w", err)
	}

	log.Printf("PostgreSQL URL: %s", pgURL)

	// Create connection pool
	pgPool, err = pgxpool.New(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL pool: %w", err)
	}

	// Verify connection
	if err := pgPool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	log.Println("PostgreSQL container ready")
	return nil
}

// setupMongoDB starts a MongoDB container and creates the client.
func setupMongoDB(ctx context.Context) error {
	var err error

	log.Println("Starting MongoDB container...")
	mongoContainer, err = mongodb.Run(ctx, "mongo:7", mongodb.WithReplicaSet("rs"))
	if err != nil {
		return fmt.Errorf("failed to start MongoDB container: %w", err)
	}

	// Get connection string
	mongoURL, err = mongoContainer.ConnectionString(ctx)
	if err != nil {
		return fmt.Errorf("failed to get MongoDB connection string: %w", err)
	}
	mongoURL, err = withDirectMongoConnection(mongoURL)
	if err != nil {
		return fmt.Errorf("failed to normalize MongoDB connection string: %w", err)
	}

	log.Printf("MongoDB URL: %s", mongoURL)

	// Create client
	mongoClient, err = mongo.Connect(options.Client().ApplyURI(mongoURL).SetDirect(true))
	if err != nil {
		return fmt.Errorf("failed to create MongoDB client: %w", err)
	}

	// Verify connection
	if err := mongoClient.Ping(ctx, nil); err != nil {
		return fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	// Get database reference
	mongoDatabase = mongoClient.Database("gomodel_test")

	log.Println("MongoDB container ready")
	return nil
}

// cleanup terminates all containers and connections.
func cleanup() {
	log.Println("Cleaning up test resources...")

	if pgPool != nil {
		pgPool.Close()
	}

	if pgContainer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pgContainer.Terminate(ctx); err != nil {
			log.Printf("Failed to terminate PostgreSQL container: %v", err)
		}
	}

	if mongoClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := mongoClient.Disconnect(ctx); err != nil {
			log.Printf("Failed to disconnect MongoDB client: %v", err)
		}
	}

	if mongoContainer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := mongoContainer.Terminate(ctx); err != nil {
			log.Printf("Failed to terminate MongoDB container: %v", err)
		}
	}

	log.Println("Cleanup complete")
}

// GetPostgreSQLPool returns the PostgreSQL connection pool for tests.
func GetPostgreSQLPool() *pgxpool.Pool {
	return pgPool
}

// GetPostgreSQLURL returns the PostgreSQL connection URL.
func GetPostgreSQLURL() string {
	return pgURL
}

// GetMongoDatabase returns the MongoDB database for tests.
func GetMongoDatabase() *mongo.Database {
	return mongoDatabase
}

// GetMongoURL returns the MongoDB connection URL.
func GetMongoURL() string {
	return mongoURL
}

// GetTestContext returns the shared test context.
func GetTestContext() context.Context {
	return testCtx
}

func withDirectMongoConnection(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("directConnection", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
