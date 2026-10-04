package litellmmigrate

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// liteLLMTestDatabase creates an empty schema and returns a pool on it plus a
// connection URL that resolves table names there.
func liteLLMTestDatabase(t *testing.T) (exec func(sql string), dbURL string) {
	t.Helper()
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return nil, ""
	}
	ctx := context.Background()
	var schema string
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	parsed, err := url.Parse(strings.TrimSpace(os.Getenv(sqlxtest.PostgresURLEnv)))
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return func(sql string) {
		_, err := pool.Exec(ctx, sql)
		require.NoError(t, err)
	}, parsed.String()
}

func TestReadDatabase(t *testing.T) {
	exec, dbURL := liteLLMTestDatabase(t)
	// An older LiteLLM schema: no organization table, fewer columns.
	exec(`CREATE TABLE "LiteLLM_VerificationToken" (token TEXT PRIMARY KEY, key_name TEXT, key_alias TEXT, spend DOUBLE PRECISION NOT NULL DEFAULT 0,
		expires TIMESTAMP(3), models TEXT[], user_id TEXT, team_id TEXT, metadata JSONB, blocked BOOLEAN, max_budget DOUBLE PRECISION,
		budget_duration TEXT, tpm_limit BIGINT, rpm_limit BIGINT)`)
	exec(`CREATE TABLE "LiteLLM_TeamTable" (team_id TEXT PRIMARY KEY, team_alias TEXT, models TEXT[], spend DOUBLE PRECISION NOT NULL DEFAULT 0)`)
	exec(`CREATE TABLE "LiteLLM_UserTable" (user_id TEXT PRIMARY KEY, models TEXT[])`)
	exec(`INSERT INTO "LiteLLM_VerificationToken" (token, key_name, key_alias, expires, models, team_id, metadata, max_budget, budget_duration, rpm_limit)
		VALUES ('abc123', 'sk-...wxyz', 'prod', '2026-11-02 23:53:06.993', '{gpt-4o}', 't1', '{"tags":["prod"]}', 50, '1d', 60)`)
	exec(`INSERT INTO "LiteLLM_TeamTable" (team_id, team_alias, models, spend) VALUES ('t1', 'Search', '{}', 1.5)`)

	db, err := ReadDatabase(context.Background(), dbURL)
	require.NoError(t, err)
	require.Len(t, db.Keys, 1)
	key := db.Keys[0]
	assert.Equal(t, "abc123", key.Token)
	assert.Equal(t, "prod", *key.KeyAlias)
	assert.Equal(t, []string{"gpt-4o"}, key.Models)
	assert.Equal(t, time.Date(2026, 11, 2, 23, 53, 6, 993000000, time.UTC), key.Expires.Time)
	assert.Equal(t, map[string]any{"tags": []any{"prod"}}, key.Metadata)
	assert.Equal(t, 50.0, *key.MaxBudget)
	assert.Equal(t, int64(60), *key.RPMLimit)
	assert.Nil(t, key.TPMLimit)
	assert.Nil(t, key.MaxParallelRequests, "a column this LiteLLM version lacks reads as unset")
	require.Len(t, db.Teams, 1)
	assert.Equal(t, 1.5, db.Teams[0].Spend)
	assert.Empty(t, db.Organizations, "a missing table reads as empty")
	assert.Empty(t, db.Users)
}

func TestReadDatabase_RejectsOtherDatabases(t *testing.T) {
	_, dbURL := liteLLMTestDatabase(t)
	_, err := ReadDatabase(context.Background(), dbURL)
	require.ErrorContains(t, err, "not a LiteLLM proxy database")
}
