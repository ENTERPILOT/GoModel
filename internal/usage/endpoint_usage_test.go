package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/enterpilot/gomodel/internal/core"
)

func seedEndpointUsageFixture(t *testing.T) (*SQLReader, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store, err := newSQLiteStore(db, 0)
	require.NoError(t, err)

	cost := func(value float64) *float64 { return &value }
	ts := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	entry := func(id, endpoint string, tokens int, total *float64) *UsageEntry {
		return &UsageEntry{
			ID: id, RequestID: "req-" + id, ProviderID: "p-" + id, Timestamp: ts,
			Model: "gpt-5", Provider: "openai", Endpoint: endpoint,
			InputTokens: tokens, OutputTokens: tokens, TotalTokens: 2 * tokens,
			TotalCost: total,
		}
	}
	localHit := entry("responses-local", "/v1/responses", 7, nil)
	localHit.CacheType = CacheTypeExact

	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
		entry("chat", "/v1/chat/completions", 10, cost(1)),
		entry("messages", "/v1/messages", 20, cost(2)),
		entry("messages-2", "/v1/messages", 5, nil),
		entry("embeddings", "/v1/embeddings", 30, cost(0.5)),
		entry("speech", "/v1/audio/speech", 40, cost(4)),
		entry("mcp", "/mcp/github", 0, nil),
		entry("mcp-lookalike", "/mcpx", 1, nil),
		entry("passthrough", "/p/openai/v1/chat/completions", 50, cost(5)),
		localHit,
	}))

	reader, err := newSQLiteReader(db)
	require.NoError(t, err)
	return reader, ctx
}

func TestSQLiteGetUsageByEndpoint(t *testing.T) {
	reader, ctx := seedEndpointUsageFixture(t)

	rows, err := reader.GetUsageByEndpoint(ctx, UsageQueryParams{})
	require.NoError(t, err)

	byEndpoint := map[string]EndpointUsage{}
	for _, row := range rows {
		byEndpoint[row.Endpoint] = row
	}
	require.Len(t, byEndpoint, 8)

	messages := byEndpoint["/v1/messages"]
	assert.Equal(t, "chat_completions", messages.Operation, "/v1/messages folds into chat")
	assert.Equal(t, 2, messages.Requests)
	assert.Equal(t, int64(50), messages.TotalTokens)
	require.NotNil(t, messages.TotalCost)
	assert.InDelta(t, 2, *messages.TotalCost, 1e-12)

	assert.Equal(t, "chat_completions", byEndpoint["/v1/chat/completions"].Operation)
	assert.Equal(t, "embeddings", byEndpoint["/v1/embeddings"].Operation)
	assert.Equal(t, "audio_speech", byEndpoint["/v1/audio/speech"].Operation)
	assert.Equal(t, "mcp", byEndpoint["/mcp/github"].Operation)
	assert.Nil(t, byEndpoint["/mcp/github"].TotalCost, "unpriced rows keep a nil cost")
	assert.Equal(t, "provider_passthrough", byEndpoint["/p/openai/v1/chat/completions"].Operation)
	assert.Empty(t, byEndpoint["/mcpx"].Operation, "unclassified endpoints carry no operation")

	// Served only from the local cache: the uncached pass never sees it, the
	// cache fold materializes it.
	local := byEndpoint["/v1/responses"]
	assert.Equal(t, "responses", local.Operation)
	assert.Equal(t, 1, local.Requests)
	assert.Equal(t, int64(0), local.InputTokens)
	assert.Equal(t, int64(7), local.LocalCachedInputTokens)
}

func TestSQLiteUsageOperationAndEndpointFilters(t *testing.T) {
	reader, ctx := seedEndpointUsageFixture(t)

	tests := []struct {
		name         string
		params       UsageQueryParams
		wantRequests int
		wantTokens   int64
	}{
		{
			name:         "chat folds chat completions and messages",
			params:       UsageQueryParams{Operations: []core.Operation{core.OperationChatCompletions}},
			wantRequests: 3,
			wantTokens:   70,
		},
		{
			name:         "several operations",
			params:       UsageQueryParams{Operations: []core.Operation{core.OperationEmbeddings, core.OperationAudioSpeech}},
			wantRequests: 2,
			wantTokens:   140,
		},
		{
			name:         "prefix matches the subtree only",
			params:       UsageQueryParams{Operations: []core.Operation{core.OperationMCP}},
			wantRequests: 1,
			wantTokens:   0,
		},
		{
			name:         "exact endpoint",
			params:       UsageQueryParams{Endpoints: []string{"/v1/messages"}},
			wantRequests: 2,
			wantTokens:   50,
		},
		{
			name:         "several endpoints",
			params:       UsageQueryParams{Endpoints: []string{"/v1/messages", "/v1/embeddings"}},
			wantRequests: 3,
			wantTokens:   110,
		},
		{
			name:         "endpoint and operation combine",
			params:       UsageQueryParams{Endpoints: []string{"/v1/messages"}, Operations: []core.Operation{core.OperationEmbeddings}},
			wantRequests: 0,
			wantTokens:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := reader.GetSummary(ctx, tt.params)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRequests, summary.TotalRequests)
			assert.Equal(t, tt.wantTokens, summary.TotalTokens)

			params := tt.params
			params.CacheMode = CacheModeAll
			log, err := reader.GetUsageLog(ctx, UsageLogParams{UsageQueryParams: params})
			require.NoError(t, err)
			assert.Equal(t, tt.wantRequests, log.Total)
		})
	}
}

func TestSQLiteUsageLogSearchMatchesEndpoint(t *testing.T) {
	reader, ctx := seedEndpointUsageFixture(t)

	log, err := reader.GetUsageLog(ctx, UsageLogParams{Search: "audio/speech"})
	require.NoError(t, err)
	require.Equal(t, 1, log.Total)
	require.Len(t, log.Entries, 1)
	assert.Equal(t, "/v1/audio/speech", log.Entries[0].Endpoint)
}

func TestOperationFiltersWithoutPathsMatchNothing(t *testing.T) {
	unknown := []core.Operation{"unknown"}

	condition, args := operationCondition(unknown)
	assert.Equal(t, "1 = 0", condition)
	assert.Empty(t, args)

	assert.Equal(t, bson.D{{Key: "endpoint", Value: bson.D{{Key: "$in", Value: bson.A{}}}}}, mongoOperationFilter(unknown))

	reader, ctx := seedEndpointUsageFixture(t)
	summary, err := reader.GetSummary(ctx, UsageQueryParams{Operations: unknown})
	require.NoError(t, err)
	assert.Equal(t, 0, summary.TotalRequests)
}

func TestOperationCondition(t *testing.T) {
	condition, args := operationCondition([]core.Operation{core.OperationEmbeddings, core.OperationBatches})

	assert.Equal(t, `(endpoint IN (?,?,?,?) OR endpoint LIKE ? ESCAPE '\' OR endpoint LIKE ? ESCAPE '\')`, condition)
	assert.Equal(t, []any{
		"/v1/embeddings", "/v1/embeddings/", "/v1/batches", "/v1/messages/batches",
		"/v1/batches/%",
		"/v1/messages/batches/%",
	}, args)
}
