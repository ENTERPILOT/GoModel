package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

// legacyUsageTable is a usage table from before the auth_key_id column.
const legacyUsageTable = `CREATE TABLE usage (
	id TEXT PRIMARY KEY, request_id TEXT NOT NULL, provider_id TEXT NOT NULL,
	timestamp DATETIME NOT NULL, model TEXT NOT NULL, provider TEXT NOT NULL,
	endpoint TEXT NOT NULL, input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
	raw_data JSON)`

func TestSQLiteUsageAuthKeyIDRoundTripAndFilter(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// A table created before auth_key_id existed gains the column on startup.
	_, err = db.Exec(legacyUsageTable)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, endpoint, total_tokens)
		VALUES ('legacy', 'r-legacy', 'p-legacy', '2026-08-08T11:00:00Z', 'gpt-5', 'openai', '/v1/chat/completions', 1)`)
	require.NoError(t, err)

	store, err := newSQLiteStore(db, 0)
	require.NoError(t, err)
	reader, err := newSQLiteReader(db)
	require.NoError(t, err)

	cost := func(value float64) *float64 { return &value }
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	entries := []*UsageEntry{
		{
			ID: "a1", RequestID: "r1", ProviderID: "p1", Timestamp: now,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			AuthKeyID: " key-a ", InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
			InputCost: cost(0.10), OutputCost: cost(0.05), TotalCost: cost(0.15),
		},
		{
			ID: "a2", RequestID: "r2", ProviderID: "p2", Timestamp: now.Add(time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			AuthKeyID: "key-a", InputTokens: 20, OutputTokens: 10, TotalTokens: 30,
			InputCost: cost(0.20), OutputCost: cost(0.10), TotalCost: cost(0.30),
		},
		{
			ID: "b1", RequestID: "r3", ProviderID: "p3", Timestamp: now.Add(2 * time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			AuthKeyID: "key-b", InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
			InputCost: cost(1), OutputCost: cost(0.5), TotalCost: cost(1.5),
		},
	}
	require.NoError(t, store.WriteBatch(context.Background(), entries))

	ctx := context.Background()
	summary, err := reader.GetSummary(ctx, UsageQueryParams{AuthKeyID: "key-a"})
	require.NoError(t, err)
	assert.Equal(t, 2, summary.TotalRequests)
	assert.Equal(t, int64(45), summary.TotalTokens)
	require.NotNil(t, summary.TotalCost)
	assert.InDelta(t, 0.45, *summary.TotalCost, 1e-12)

	logResult, err := reader.GetUsageLog(ctx, UsageLogParams{
		AuthKeyID: "key-b", CacheMode: CacheModeAll,
	})
	require.NoError(t, err)
	require.Equal(t, 1, logResult.Total)
	require.Len(t, logResult.Entries, 1)
	assert.Equal(t, "b1", logResult.Entries[0].ID)
	assert.Equal(t, "key-b", logResult.Entries[0].AuthKeyID)

	byRequest, err := reader.GetUsageByRequestIDs(ctx, []string{"r1", "r-legacy"})
	require.NoError(t, err)
	require.Len(t, byRequest["r1"], 1)
	assert.Equal(t, "key-a", byRequest["r1"][0].AuthKeyID, "stored key id is trimmed")
	require.Len(t, byRequest["r-legacy"], 1)
	assert.Empty(t, byRequest["r-legacy"][0].AuthKeyID, "legacy rows read back without a key")
}

func TestNewMigratesSchemaWhenUsageDisabled(t *testing.T) {
	store, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "gomodel.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.DB().Exec(legacyUsageTable)
	require.NoError(t, err)

	cfg := &config.Config{}
	cfg.Usage.Enabled = false
	result, err := New(context.Background(), cfg, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Close() })

	// History recorded while tracking was on stays readable after an upgrade.
	reader, err := NewReader(store)
	require.NoError(t, err)
	_, err = reader.GetUsageLog(context.Background(), UsageLogParams{})
	require.NoError(t, err)
}
