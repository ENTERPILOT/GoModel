package usage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
)

func newImportSQLite(t *testing.T, name string) (string, storage.SQLiteStorage) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	store, err := storage.NewSQLite(storage.SQLiteConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return path, store
}

func TestImportSQLiteFile_CopiesRowsAndSkipsExisting(t *testing.T) {
	ctx := context.Background()
	srcPath, src := newImportSQLite(t, "history #1 100%.db")
	srcStore, err := NewSQLiteStore(src.DB(), 0)
	require.NoError(t, err)

	ts := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	cost, billed, markup := 0.25, 0.30, 20.0
	entries := []*UsageEntry{
		{
			ID: "u-1", RequestID: "req-1", ProviderID: "chatcmpl-1", Timestamp: ts,
			Model: "gpt-4o-mini", Provider: "openai", ProviderName: "openai-eu", Endpoint: "/v1/chat/completions",
			UserPath: "/team/a", SessionID: "s-1", AuthKeyID: "key-1", Labels: []string{"prod"},
			InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
			RawData:   map[string]any{"cached_tokens": float64(4)},
			TotalCost: &cost, CostSource: "catalog", MarkupPercent: &markup, MarkupSelector: "openai", BilledCost: &billed,
		},
		{ID: "u-2", RequestID: "req-2", Timestamp: ts.Add(time.Minute), Model: "claude", Provider: "anthropic", Endpoint: "/v1/messages"},
	}
	require.NoError(t, srcStore.WriteBatch(ctx, entries))

	_, dst := newImportSQLite(t, "destination.db")
	n, err := ImportSQLiteFile(ctx, srcPath, dst, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	reader, err := NewSQLiteReader(dst.DB())
	require.NoError(t, err)
	got, err := reader.GetUsageByRequestIDs(ctx, []string{"req-1", "req-2"})
	require.NoError(t, err)
	require.Len(t, got["req-1"], 1)
	first := got["req-1"][0]
	assert.Equal(t, "u-1", first.ID)
	assert.True(t, ts.Equal(first.Timestamp))
	assert.Equal(t, "openai-eu", first.ProviderName)
	assert.Equal(t, "/team/a", first.UserPath)
	assert.Equal(t, "key-1", first.AuthKeyID)
	assert.Equal(t, []string{"prod"}, first.Labels)
	assert.Equal(t, 15, first.TotalTokens)
	assert.Equal(t, float64(4), first.RawData["cached_tokens"])
	require.NotNil(t, first.BilledCost)
	assert.InDelta(t, billed, *first.BilledCost, 1e-9)
	assert.Len(t, got["req-2"], 1)

	// A second run adds nothing.
	n, err = ImportSQLiteFile(ctx, srcPath, dst, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	var count int
	require.NoError(t, dst.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM usage").Scan(&count))
	assert.Equal(t, 2, count)
}

func TestImportSQLiteFile_OlderSourceSchema(t *testing.T) {
	ctx := context.Background()
	srcPath, src := newImportSQLite(t, "old.db")
	_, err := src.DB().ExecContext(ctx, `CREATE TABLE usage (
		id TEXT PRIMARY KEY, request_id TEXT NOT NULL, provider_id TEXT NOT NULL, timestamp DATETIME NOT NULL,
		model TEXT NOT NULL, provider TEXT NOT NULL, endpoint TEXT NOT NULL,
		input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0, raw_data JSON)`)
	require.NoError(t, err)
	_, err = src.DB().ExecContext(ctx, `INSERT INTO usage VALUES
		('old-1', 'req-old', 'p', '2025-01-02T03:04:05Z', 'gpt-4o', 'openai', '/v1/chat/completions', 3, 4, 7, NULL)`)
	require.NoError(t, err)

	_, dst := newImportSQLite(t, "destination.db")
	n, err := ImportSQLiteFile(ctx, srcPath, dst, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	reader, err := NewSQLiteReader(dst.DB())
	require.NoError(t, err)
	got, err := reader.GetUsageByRequestIDs(ctx, []string{"req-old"})
	require.NoError(t, err)
	require.Len(t, got["req-old"], 1)
	assert.Equal(t, 7, got["req-old"][0].TotalTokens)
	assert.Nil(t, got["req-old"][0].TotalCost)
}

func TestImportSQLiteFile_SourceErrors(t *testing.T) {
	ctx := context.Background()
	_, dst := newImportSQLite(t, "destination.db")

	_, err := ImportSQLiteFile(ctx, filepath.Join(t.TempDir(), "missing.db"), dst, 0)
	require.ErrorContains(t, err, "open source database")

	emptyPath, _ := newImportSQLite(t, "empty.db")
	_, err = ImportSQLiteFile(ctx, emptyPath, dst, 0)
	require.ErrorContains(t, err, "no usage table")
}

func TestOnlyDuplicateKeys(t *testing.T) {
	writeErrs := func(codes ...int) []mongo.BulkWriteError {
		out := make([]mongo.BulkWriteError, len(codes))
		for i, code := range codes {
			out[i] = mongo.BulkWriteError{Code: code}
		}
		return out
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "duplicates", err: &PartialWriteError{Cause: mongo.BulkWriteException{WriteErrors: writeErrs(11000, 11000)}}, want: true},
		{name: "mixed", err: &PartialWriteError{Cause: mongo.BulkWriteException{WriteErrors: writeErrs(11000, 2)}}},
		{name: "write concern", err: &PartialWriteError{Cause: mongo.BulkWriteException{
			WriteErrors: writeErrs(11000), WriteConcernError: &mongo.WriteConcernError{Code: 64},
		}}},
		{name: "other error", err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, onlyDuplicateKeys(tt.err))
		})
	}
}
