//go:build integration

// Package dbassert provides database assertion helpers for integration tests.
// It supports querying and validating request logs and usage entries in PostgreSQL and MongoDB.
package dbassert

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/requestlog"
)

// RequestLogEntry mirrors requestlog.LogEntry for test assertions.
// We use a separate type to avoid coupling tests to internal implementation details.
type RequestLogEntry struct {
	ID                string
	Timestamp         time.Time
	DurationNs        int64
	Model             string
	Provider          string
	WorkflowVersionID string
	CacheType         string
	StatusCode        int
	RequestID         string
	AuthKeyID         string
	AuthMethod        string
	ClientIP          string
	Method            string
	Path              string
	UserPath          string
	Stream            bool
	ErrorType         string
	Data              *requestlog.LogData
}

// QueryRequestLogsByRequestID queries request logs by request ID from PostgreSQL.
func QueryRequestLogsByRequestID(t *testing.T, pool *pgxpool.Pool, requestID string) []RequestLogEntry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `
		SELECT id, timestamp, duration_ns, requested_model, provider, workflow_version_id, cache_type, status_code,
		       request_id, auth_key_id, auth_method, client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE request_id = $1
		ORDER BY timestamp ASC
	`

	rows, err := pool.Query(ctx, query, requestID)
	require.NoError(t, err, "failed to query request logs")
	defer rows.Close()

	var entries []RequestLogEntry
	for rows.Next() {
		var entry RequestLogEntry
		var workflowVersionID sql.NullString
		var cacheType sql.NullString
		var authKeyID sql.NullString
		var authMethod sql.NullString
		var userPathNull sql.NullString
		var dataJSON []byte
		err := rows.Scan(
			&entry.ID, &entry.Timestamp, &entry.DurationNs,
			&entry.Model, &entry.Provider, &workflowVersionID, &cacheType, &entry.StatusCode,
			&entry.RequestID, &authKeyID, &authMethod, &entry.ClientIP, &entry.Method,
			&entry.Path, &userPathNull, &entry.Stream, &entry.ErrorType, &dataJSON,
		)
		require.NoError(t, err, "failed to scan request log row")

		if workflowVersionID.Valid {
			entry.WorkflowVersionID = workflowVersionID.String
		}
		if cacheType.Valid {
			entry.CacheType = cacheType.String
		}
		if authKeyID.Valid {
			entry.AuthKeyID = authKeyID.String
		}
		if authMethod.Valid {
			entry.AuthMethod = authMethod.String
		}
		if userPathNull.Valid {
			entry.UserPath = userPathNull.String
		}
		if dataJSON != nil {
			entry.Data = unmarshalLogData(t, dataJSON)
		}
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Err(), "error iterating request log rows")

	return entries
}

// QueryRequestLogsByRequestIDMongo queries request logs by request ID from MongoDB.
func QueryRequestLogsByRequestIDMongo(t *testing.T, db *mongo.Database, requestID string) []RequestLogEntry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := db.Collection("audit_logs")
	filter := bson.M{"request_id": requestID}

	cursor, err := collection.Find(ctx, filter)
	require.NoError(t, err, "failed to query request logs from MongoDB")
	defer cursor.Close(ctx)

	var entries []RequestLogEntry
	for cursor.Next(ctx) {
		var doc bson.M
		err := cursor.Decode(&doc)
		require.NoError(t, err, "failed to decode request log document")

		entry := bsonToRequestLogEntry(t, doc)
		entries = append(entries, entry)
	}
	require.NoError(t, cursor.Err(), "error iterating request log cursor")

	return entries
}

// ClearRequestLogs deletes all request log entries from PostgreSQL.
func ClearRequestLogs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, "DELETE FROM audit_logs")
	require.NoError(t, err, "failed to clear request logs")
}

// bsonToRequestLogEntry converts a BSON document to an RequestLogEntry.
func bsonToRequestLogEntry(t *testing.T, doc bson.M) RequestLogEntry {
	t.Helper()
	entry := RequestLogEntry{}

	if v, ok := doc["_id"].(string); ok {
		entry.ID = v
	}
	if v, ok := doc["timestamp"].(time.Time); ok {
		entry.Timestamp = v
	} else if v, ok := doc["timestamp"].(bson.DateTime); ok {
		entry.Timestamp = v.Time()
	}
	if v, ok := doc["duration_ns"].(int64); ok {
		entry.DurationNs = v
	} else if v, ok := doc["duration_ns"].(int32); ok {
		entry.DurationNs = int64(v)
	}
	if v, ok := doc["requested_model"].(string); ok {
		entry.Model = v
	} else if v, ok := doc["model"].(string); ok {
		entry.Model = v
	}
	if v, ok := doc["provider"].(string); ok {
		entry.Provider = v
	}
	if v, ok := doc["workflow_version_id"].(string); ok {
		entry.WorkflowVersionID = v
	}
	if v, ok := doc["cache_type"].(string); ok {
		entry.CacheType = v
	}
	if v, ok := doc["status_code"].(int32); ok {
		entry.StatusCode = int(v)
	} else if v, ok := doc["status_code"].(int64); ok {
		entry.StatusCode = int(v)
	}
	if v, ok := doc["request_id"].(string); ok {
		entry.RequestID = v
	}
	if v, ok := doc["auth_key_id"].(string); ok {
		entry.AuthKeyID = v
	}
	if v, ok := doc["auth_method"].(string); ok {
		entry.AuthMethod = v
	}
	if v, ok := doc["client_ip"].(string); ok {
		entry.ClientIP = v
	}
	if v, ok := doc["method"].(string); ok {
		entry.Method = v
	}
	if v, ok := doc["path"].(string); ok {
		entry.Path = v
	}
	if v, ok := doc["user_path"].(string); ok {
		entry.UserPath = v
	}
	if v, ok := doc["stream"].(bool); ok {
		entry.Stream = v
	}
	if v, ok := doc["error_type"].(string); ok {
		entry.ErrorType = v
	}

	// Handle data field
	if dataDoc, ok := doc["data"].(bson.M); ok {
		entry.Data = bsonToLogData(dataDoc)
	}

	return entry
}

// bsonToLogData converts a BSON document to requestlog.LogData.
func bsonToLogData(doc bson.M) *requestlog.LogData {
	data := &requestlog.LogData{}

	if v, ok := doc["user_agent"].(string); ok {
		data.UserAgent = v
	}
	if v, ok := doc["api_key_hash"].(string); ok {
		data.APIKeyHash = v
	}
	if v, ok := doc["temperature"].(float64); ok {
		data.Temperature = &v
	}
	if v, ok := doc["max_tokens"].(int32); ok {
		val := int(v)
		data.MaxTokens = &val
	} else if v, ok := doc["max_tokens"].(int64); ok {
		val := int(v)
		data.MaxTokens = &val
	}
	if v, ok := doc["error_message"].(string); ok {
		data.ErrorMessage = v
	}
	if v, ok := doc["request_headers"].(bson.M); ok {
		data.RequestHeaders = bsonMapToStringMap(v)
	}
	if v, ok := doc["response_headers"].(bson.M); ok {
		data.ResponseHeaders = bsonMapToStringMap(v)
	}
	if v := doc["request_body"]; v != nil {
		data.RequestBody = v
	}
	if v := doc["response_body"]; v != nil {
		data.ResponseBody = v
	}
	if v, ok := doc["request_body_too_big_to_handle"].(bool); ok {
		data.RequestBodyTooBigToHandle = v
	}
	if v, ok := doc["response_body_too_big_to_handle"].(bool); ok {
		data.ResponseBodyTooBigToHandle = v
	}

	return data
}

// bsonMapToStringMap converts a bson.M to map[string]string.
func bsonMapToStringMap(m bson.M) map[string]string {
	result := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}
