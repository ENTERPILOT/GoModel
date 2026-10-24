package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteReaderGetDailyUsage_GroupsByConfiguredTimeZone(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "entry-1",
			RequestID:    "req-1",
			ProviderID:   "provider-1",
			Timestamp:    time.Date(2026, 1, 15, 22, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
		{
			ID:           "entry-2",
			RequestID:    "req-2",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  20,
			OutputTokens: 10,
			TotalTokens:  30,
		},
		{
			ID:           "entry-3",
			RequestID:    "req-3",
			ProviderID:   "provider-3",
			Timestamp:    time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  25,
			OutputTokens: 15,
			TotalTokens:  40,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		Interval:  "daily",
		TimeZone:  "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.Len(t, daily, 1)
	assert.Equal(t, "2026-01-16", daily[0].Date)
	assert.Equal(t, 2, daily[0].Requests)
	assert.Equal(t, int64(70), daily[0].TotalTokens)
}
