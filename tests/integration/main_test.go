//go:build integration

// Package integration provides integration tests that verify database state
// after HTTP requests. Tests run against real PostgreSQL and MongoDB instances
// managed through the Docker CLI.
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
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	// PostgreSQL resources
	pgContainer *dockerContainer
	pgPool      *pgxpool.Pool
	pgURL       string

	// MongoDB resources
	mongoContainer *dockerContainer
	mongoClient    *mongo.Client
	mongoDatabase  *mongo.Database
	mongoURL       string

	// Test context
	testCtx    context.Context
	cancelFunc context.CancelFunc
)

const mongoReplicaSetName = "rs"

// TestMain sets up and tears down the Docker-backed test databases.
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
	pgContainer, err = dockerRunDetached(
		ctx,
		[]string{
			"-P",
			"-e", "POSTGRES_DB=gomodel_test",
			"-e", "POSTGRES_USER=test",
			"-e", "POSTGRES_PASSWORD=test",
		},
		"postgres:16-alpine",
	)
	if err != nil {
		return fmt.Errorf("failed to start PostgreSQL container: %w", err)
	}

	port, err := pgContainer.hostPort(ctx, "5432/tcp")
	if err != nil {
		return fmt.Errorf("failed to get PostgreSQL port: %w", err)
	}
	pgURL = fmt.Sprintf("postgres://test:test@%s:%s/gomodel_test?sslmode=disable", dockerPublishedHost(), port)

	log.Printf("PostgreSQL URL: %s", pgURL)

	// Create connection pool
	pgPool, err = pgxpool.New(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL pool: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := waitForCondition(readyCtx, 2*time.Second, func(attemptCtx context.Context) error {
		return pgPool.Ping(attemptCtx)
	}); err != nil {
		return fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	log.Println("PostgreSQL container ready")
	return nil
}

// setupMongoDB starts a MongoDB container and creates the client.
func setupMongoDB(ctx context.Context) error {
	var err error

	log.Println("Starting MongoDB container...")
	mongoContainer, err = dockerRunDetached(
		ctx,
		[]string{"-P"},
		"mongo:7",
		"--replSet", mongoReplicaSetName,
		"--bind_ip_all",
	)
	if err != nil {
		return fmt.Errorf("failed to start MongoDB container: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := waitForCondition(readyCtx, 2*time.Second, func(attemptCtx context.Context) error {
		_, execErr := mongoContainer.exec(attemptCtx,
			"mongosh",
			"--quiet",
			"--eval",
			"db.adminCommand({ ping: 1 }).ok",
		)
		return execErr
	}); err != nil {
		return fmt.Errorf("failed to wait for MongoDB shell readiness: %w", err)
	}

	containerIP, err := mongoContainer.ip(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect MongoDB container IP: %w", err)
	}
	if _, err := mongoContainer.exec(
		ctx,
		"mongosh",
		"--quiet",
		"--eval",
		fmt.Sprintf(
			"rs.initiate({ _id: '%s', members: [ { _id: 0, host: '%s:27017' } ] })",
			mongoReplicaSetName,
			containerIP,
		),
	); err != nil {
		return fmt.Errorf("failed to initiate MongoDB replica set: %w", err)
	}

	if err := waitForCondition(readyCtx, 2*time.Second, func(attemptCtx context.Context) error {
		_, execErr := mongoContainer.exec(
			attemptCtx,
			"mongosh",
			"--quiet",
			"--eval",
			"const status = rs.status(); if (status.ok !== 1) { quit(1) }",
		)
		return execErr
	}); err != nil {
		return fmt.Errorf("failed to wait for MongoDB replica set readiness: %w", err)
	}

	port, err := mongoContainer.hostPort(ctx, "27017/tcp")
	if err != nil {
		return fmt.Errorf("failed to get MongoDB port: %w", err)
	}
	mongoURL = fmt.Sprintf("mongodb://%s:%s/?replicaSet=%s", dockerPublishedHost(), port, mongoReplicaSetName)
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

	if err := waitForCondition(readyCtx, 2*time.Second, func(attemptCtx context.Context) error {
		return mongoClient.Ping(attemptCtx, nil)
	}); err != nil {
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
		if err := pgContainer.terminate(ctx); err != nil {
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
		if err := mongoContainer.terminate(ctx); err != nil {
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
