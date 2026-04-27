//go:build integration

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
)

// UsageEntry mirrors usage.UsageEntry for test assertions.
type UsageEntry struct {
	ID           string
	RequestID    string
	ProviderID   string
	Timestamp    time.Time
	Model        string
	Provider     string
	Endpoint     string
	UserPath     string
	CacheType    string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	InputCost    *float64
	OutputCost   *float64
	TotalCost    *float64
	RawData      map[string]any
}

// QueryUsageByRequestID queries usage entries by request ID from PostgreSQL.
func QueryUsageByRequestID(t *testing.T, pool *pgxpool.Pool, requestID string) []UsageEntry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `
		SELECT id, request_id, provider_id, timestamp, model, provider, endpoint, user_path, cache_type,
		       input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost, raw_data
		FROM usage
		WHERE request_id = $1
		ORDER BY timestamp ASC
	`

	rows, err := pool.Query(ctx, query, requestID)
	require.NoError(t, err, "failed to query usage entries")
	defer rows.Close()

	var entries []UsageEntry
	for rows.Next() {
		var entry UsageEntry
		var cacheType sql.NullString
		var inputCost sql.NullFloat64
		var outputCost sql.NullFloat64
		var totalCost sql.NullFloat64
		var rawDataJSON []byte
		err := rows.Scan(
			&entry.ID, &entry.RequestID, &entry.ProviderID,
			&entry.Timestamp, &entry.Model, &entry.Provider, &entry.Endpoint, &entry.UserPath, &cacheType,
			&entry.InputTokens, &entry.OutputTokens, &entry.TotalTokens, &inputCost, &outputCost, &totalCost, &rawDataJSON,
		)
		require.NoError(t, err, "failed to scan usage row")

		if cacheType.Valid {
			entry.CacheType = cacheType.String
		}
		if inputCost.Valid {
			entry.InputCost = &inputCost.Float64
		}
		if outputCost.Valid {
			entry.OutputCost = &outputCost.Float64
		}
		if totalCost.Valid {
			entry.TotalCost = &totalCost.Float64
		}
		if rawDataJSON != nil {
			entry.RawData = unmarshalRawData(t, rawDataJSON)
		}
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Err(), "error iterating usage rows")

	return entries
}

// QueryUsageByRequestIDMongo queries usage entries by request ID from MongoDB.
func QueryUsageByRequestIDMongo(t *testing.T, db *mongo.Database, requestID string) []UsageEntry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := db.Collection("usage")
	filter := bson.M{"request_id": requestID}

	cursor, err := collection.Find(ctx, filter)
	require.NoError(t, err, "failed to query usage entries from MongoDB")
	defer cursor.Close(ctx)

	var entries []UsageEntry
	for cursor.Next(ctx) {
		var doc bson.M
		err := cursor.Decode(&doc)
		require.NoError(t, err, "failed to decode usage document")

		entry := bsonToUsageEntry(t, doc)
		entries = append(entries, entry)
	}
	require.NoError(t, cursor.Err(), "error iterating usage cursor")

	return entries
}

// CountUsage returns the total count of usage entries in PostgreSQL.
func CountUsage(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var count int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM usage").Scan(&count)
	require.NoError(t, err, "failed to count usage entries")

	return count
}

// ClearUsage deletes all usage entries from PostgreSQL.
func ClearUsage(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, "DELETE FROM usage")
	require.NoError(t, err, "failed to clear usage entries")
}

// ClearUsageMongo deletes all usage entries from MongoDB.
func ClearUsageMongo(t *testing.T, db *mongo.Database) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := db.Collection("usage").DeleteMany(ctx, bson.M{})
	require.NoError(t, err, "failed to clear usage entries from MongoDB")
}

// SumTokensByModel returns total token usage grouped by model from PostgreSQL.
func SumTokensByModel(t *testing.T, pool *pgxpool.Pool) map[string]TokenSummary {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `
		SELECT model, SUM(input_tokens), SUM(output_tokens), SUM(total_tokens), COUNT(*)
		FROM usage
		GROUP BY model
	`

	rows, err := pool.Query(ctx, query)
	require.NoError(t, err, "failed to query token summary")
	defer rows.Close()

	results := make(map[string]TokenSummary)
	for rows.Next() {
		var model string
		var summary TokenSummary
		err := rows.Scan(&model, &summary.InputTokens, &summary.OutputTokens, &summary.TotalTokens, &summary.RequestCount)
		require.NoError(t, err, "failed to scan token summary row")
		results[model] = summary
	}
	require.NoError(t, rows.Err(), "error iterating token summary rows")

	return results
}

// TokenSummary holds aggregated token usage statistics.
type TokenSummary struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	RequestCount int64
}

// bsonToUsageEntry converts a BSON document to a UsageEntry.
func bsonToUsageEntry(t *testing.T, doc bson.M) UsageEntry {
	t.Helper()
	entry := UsageEntry{}

	if v, ok := doc["_id"].(string); ok {
		entry.ID = v
	}
	if v, ok := doc["request_id"].(string); ok {
		entry.RequestID = v
	}
	if v, ok := doc["provider_id"].(string); ok {
		entry.ProviderID = v
	}
	if v, ok := doc["timestamp"].(time.Time); ok {
		entry.Timestamp = v
	} else if v, ok := doc["timestamp"].(bson.DateTime); ok {
		entry.Timestamp = v.Time()
	}
	if v, ok := doc["model"].(string); ok {
		entry.Model = v
	}
	if v, ok := doc["provider"].(string); ok {
		entry.Provider = v
	}
	if v, ok := doc["endpoint"].(string); ok {
		entry.Endpoint = v
	}
	if v, ok := doc["user_path"].(string); ok {
		entry.UserPath = v
	}
	if v, ok := doc["cache_type"].(string); ok {
		entry.CacheType = v
	}
	if v, ok := doc["input_tokens"].(int32); ok {
		entry.InputTokens = int(v)
	} else if v, ok := doc["input_tokens"].(int64); ok {
		entry.InputTokens = int(v)
	}
	if v, ok := doc["output_tokens"].(int32); ok {
		entry.OutputTokens = int(v)
	} else if v, ok := doc["output_tokens"].(int64); ok {
		entry.OutputTokens = int(v)
	}
	if v, ok := doc["total_tokens"].(int32); ok {
		entry.TotalTokens = int(v)
	} else if v, ok := doc["total_tokens"].(int64); ok {
		entry.TotalTokens = int(v)
	}
	if v, ok := doc["input_cost"].(float64); ok {
		entry.InputCost = &v
	}
	if v, ok := doc["output_cost"].(float64); ok {
		entry.OutputCost = &v
	}
	if v, ok := doc["total_cost"].(float64); ok {
		entry.TotalCost = &v
	}
	if v, ok := doc["raw_data"].(bson.M); ok {
		entry.RawData = bsonToMap(v)
	}

	return entry
}

// bsonToMap converts a bson.M to a map[string]any recursively.
func bsonToMap(m bson.M) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case bson.M:
			result[k] = bsonToMap(val)
		case bson.A:
			result[k] = bsonArrayToSlice(val)
		default:
			result[k] = v
		}
	}
	return result
}

// bsonArrayToSlice converts a bson.A to []any.
func bsonArrayToSlice(a bson.A) []any {
	result := make([]any, len(a))
	for i, v := range a {
		switch val := v.(type) {
		case bson.M:
			result[i] = bsonToMap(val)
		case bson.A:
			result[i] = bsonArrayToSlice(val)
		default:
			result[i] = v
		}
	}
	return result
}
