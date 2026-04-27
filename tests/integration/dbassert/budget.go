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

	"gomodel/internal/budget"
)

// QueryBudgets returns all persisted budget rows from PostgreSQL.
func QueryBudgets(t *testing.T, pool *pgxpool.Pool) []budget.Budget {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := pool.Query(ctx, `
		SELECT user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at
		FROM budgets
		ORDER BY user_path, period_seconds
	`)
	require.NoError(t, err, "failed to query budgets")
	defer rows.Close()

	var budgets []budget.Budget
	for rows.Next() {
		var item budget.Budget
		var lastResetAt sql.NullInt64
		var createdAt int64
		var updatedAt int64
		require.NoError(t, rows.Scan(
			&item.UserPath,
			&item.PeriodSeconds,
			&item.Amount,
			&item.Source,
			&lastResetAt,
			&createdAt,
			&updatedAt,
		), "failed to scan budget row")
		if lastResetAt.Valid {
			tm := time.Unix(lastResetAt.Int64, 0).UTC()
			item.LastResetAt = &tm
		}
		item.CreatedAt = time.Unix(createdAt, 0).UTC()
		item.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		budgets = append(budgets, item)
	}
	require.NoError(t, rows.Err(), "error iterating budget rows")
	return budgets
}

// QueryBudgetsMongo returns all persisted budget documents from MongoDB.
func QueryBudgetsMongo(t *testing.T, db *mongo.Database) []budget.Budget {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := db.Collection("budgets").Find(ctx, bson.D{})
	require.NoError(t, err, "failed to query budgets from MongoDB")
	defer cursor.Close(ctx)

	var budgets []budget.Budget
	for cursor.Next(ctx) {
		var item budget.Budget
		require.NoError(t, cursor.Decode(&item), "failed to decode budget document")
		budgets = append(budgets, item)
	}
	require.NoError(t, cursor.Err(), "error iterating budget cursor")
	return budgets
}

// PostgreSQLTableExists reports whether a table exists in the public schema.
func PostgreSQLTableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)
	`, table).Scan(&exists)
	require.NoError(t, err, "failed to check whether table %s exists", table)
	return exists
}

// MongoCollectionExists reports whether a collection exists.
func MongoCollectionExists(t *testing.T, db *mongo.Database, collection string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: collection}})
	require.NoError(t, err, "failed to list MongoDB collections")
	return len(names) > 0
}

// AssertOneSeededBudget verifies the static budget seeded during app startup.
func AssertOneSeededBudget(t *testing.T, budgets []budget.Budget, userPath string, periodSeconds int64, amount float64) {
	t.Helper()
	require.Len(t, budgets, 1, "expected one seeded budget")
	got := budgets[0]
	require.Equal(t, userPath, got.UserPath)
	require.Equal(t, periodSeconds, got.PeriodSeconds)
	require.InEpsilon(t, amount, got.Amount, 0.000001)
	require.Equal(t, budget.SourceConfig, got.Source)
	require.False(t, got.CreatedAt.IsZero(), "created_at should be populated")
	require.False(t, got.UpdatedAt.IsZero(), "updated_at should be populated")
}

// QueryBudgetsForFixture returns budgets for the fixture's configured database.
func QueryBudgetsForFixture(t *testing.T, dbType string, pool *pgxpool.Pool, db *mongo.Database) []budget.Budget {
	t.Helper()
	switch dbType {
	case "postgresql":
		return QueryBudgets(t, pool)
	case "mongodb":
		return QueryBudgetsMongo(t, db)
	default:
		t.Fatalf("unsupported DB type %q", dbType)
		return nil
	}
}

// StorageObjectExists reports whether a table or collection exists.
func StorageObjectExists(t *testing.T, dbType string, pool *pgxpool.Pool, db *mongo.Database, name string) bool {
	t.Helper()
	switch dbType {
	case "postgresql":
		return PostgreSQLTableExists(t, pool, name)
	case "mongodb":
		return MongoCollectionExists(t, db, name)
	default:
		t.Fatalf("unsupported DB type %q", dbType)
		return false
	}
}
