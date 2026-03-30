package auditlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrPartialWrite indicates that a batch write only partially succeeded.
// Use errors.As to extract details about the failure.
var ErrPartialWrite = errors.New("partial write failure")

// PartialWriteError wraps a mongo.BulkWriteException with additional context
// about how many entries failed vs succeeded.
type PartialWriteError struct {
	TotalEntries int
	FailedCount  int
	Cause        mongo.BulkWriteException
}

func (e *PartialWriteError) Error() string {
	return fmt.Sprintf("partial audit log insert: %d of %d entries failed: %v",
		e.FailedCount, e.TotalEntries, e.Cause.Error())
}

func (e *PartialWriteError) Unwrap() error {
	return ErrPartialWrite
}

// Prometheus metric for audit log partial write failures
var auditLogPartialWriteFailures = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gomodel_audit_log_partial_write_failures_total",
		Help: "Total number of partial write failures when inserting audit logs to MongoDB",
	},
)

// MongoDBStore implements LogStore for MongoDB.
type MongoDBStore struct {
	collection    *mongo.Collection
	retentionDays int
}

// NewMongoDBStore creates a new MongoDB audit log store.
// It creates the collection and indexes if they don't exist.
// MongoDB handles TTL-based cleanup automatically via TTL indexes.
func NewMongoDBStore(database *mongo.Database, retentionDays int) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}

	collection := database.Collection("audit_logs")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create indexes for common queries
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "model", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "status_code", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "provider", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "execution_plan_version_id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "request_id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "auth_key_id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "client_ip", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "path", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "error_type", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "data.response_body.id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "data.request_body.previous_response_id", Value: 1}},
		},
	}

	// Add timestamp index - use TTL index if retention is configured,
	// otherwise use a regular descending index for query performance.
	// MongoDB doesn't allow multiple indexes on the same field when one is TTL.
	if retentionDays > 0 {
		ttlSeconds := int32(int64(retentionDays) * 24 * 60 * 60)
		indexes = append(indexes, mongo.IndexModel{
			Keys:    bson.D{{Key: "timestamp", Value: -1}},
			Options: options.Index().SetExpireAfterSeconds(ttlSeconds),
		})
	} else {
		indexes = append(indexes, mongo.IndexModel{
			Keys: bson.D{{Key: "timestamp", Value: -1}},
		})
	}

	_, err := collection.Indexes().CreateMany(ctx, indexes)
	if err != nil {
		// Log warning but don't fail - indexes may already exist
		slog.Warn("failed to create some MongoDB indexes", "error", err)
	}

	return &MongoDBStore{
		collection:    collection,
		retentionDays: retentionDays,
	}, nil
}

// WriteBatch writes multiple log entries to MongoDB using InsertMany.
func (s *MongoDBStore) WriteBatch(ctx context.Context, entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Convert entries to BSON documents
	docs := make([]any, len(entries))
	for i, e := range entries {
		docs[i] = e
	}

	// Use unordered insert for better performance (continues on errors)
	opts := options.InsertMany().SetOrdered(false)
	_, err := s.collection.InsertMany(ctx, docs, opts)
	if err != nil {
		// Check if it's a bulk write error with some successes
		if bulkErr, ok := errors.AsType[*mongo.BulkWriteException](err); ok {
			failedCount := len(bulkErr.WriteErrors)
			// Log for visibility
			slog.Warn("partial audit log insert failure",
				"total", len(entries),
				"failed", failedCount,
				"succeeded", len(entries)-failedCount,
			)
			// Increment metric for operators to detect data loss
			auditLogPartialWriteFailures.Inc()
			// Return distinguishable error so callers know insert was partial
			return &PartialWriteError{
				TotalEntries: len(entries),
				FailedCount:  failedCount,
				Cause:        *bulkErr,
			}
		}
		return fmt.Errorf("failed to insert audit logs: %w", err)
	}

	return nil
}

// Flush is a no-op for MongoDB as writes are synchronous.
func (s *MongoDBStore) Flush(_ context.Context) error {
	return nil
}

// Close is a no-op for MongoDB as the client is managed by the storage layer.
func (s *MongoDBStore) Close() error {
	return nil
}
