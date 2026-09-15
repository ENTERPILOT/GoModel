package auditlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

func TestSQLReaderGetLogs_IncludesFractionalStartBoundaryAndExcludesFractionalEndBoundary(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		ctx := context.Background()
		err = store.WriteBatch(ctx, []*LogEntry{
			{
				ID:             "start-boundary",
				Timestamp:      time.Date(2026, 1, 15, 23, 0, 0, 123_000_000, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
			},
			{
				ID:             "inside-range",
				Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
			},
			{
				ID:             "after-end-boundary",
				Timestamp:      time.Date(2026, 1, 16, 23, 0, 0, 123_000_000, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
			},
		})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		location, err := time.LoadLocation("Europe/Warsaw")
		require.NoError(t, err)

		result, err := reader.GetLogs(ctx, LogQueryParams{
			StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
			EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
			Limit:     10,
			Offset:    0,
		})
		require.NoError(t, err)
		require.Equal(t, 2, result.Total)
		require.Len(t, result.Entries, 2)
		require.Equal(t, "inside-range", result.Entries[0].ID)
		require.Equal(t, "start-boundary", result.Entries[1].ID)
	})
}

func TestSQLReaderGetLogs_SearchMatchesUserPath(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		ctx := context.Background()
		err = store.WriteBatch(ctx, []*LogEntry{
			{
				ID:             "team-match",
				Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				UserPath:       "/team/alpha",
			},
			{
				ID:             "other-team",
				Timestamp:      time.Date(2026, 1, 16, 11, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				UserPath:       "/org/beta",
			},
		})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		result, err := reader.GetLogs(ctx, LogQueryParams{
			Search: "team/alpha",
			Limit:  10,
		})
		require.NoError(t, err)
		require.Equal(t, 1, result.Total)
		require.Len(t, result.Entries, 1)
		require.Equal(t, "team-match", result.Entries[0].ID)
	})
}

// A full canonical UUID takes the indexed-identifier fast path: equality on
// id/request_id/auth_key_id/session_id, case-insensitively — and deliberately
// no longer the LIKE sweep over the free-text columns.
func TestSQLReaderGetLogs_SearchUUIDMatchesIdentifierColumns(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		const searchUUID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		ctx := context.Background()
		err = store.WriteBatch(ctx, []*LogEntry{
			{
				ID:             "request-id-match",
				Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				RequestID:      searchUUID,
			},
			{
				ID:             "session-id-match",
				Timestamp:      time.Date(2026, 1, 16, 11, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				SessionID:      searchUUID,
			},
			{
				// A UUID buried in an error message is not an identifier hit:
				// the fast path skips the free-text columns on purpose.
				ID:             "error-message-only",
				Timestamp:      time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				ErrorType:      "provider_error",
				Data:           &LogData{ErrorMessage: "upstream rejected request " + searchUUID},
			},
		})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		// Uppercase paste must still find the lowercase stored identifiers.
		result, err := reader.GetLogs(ctx, LogQueryParams{
			Search: strings.ToUpper(searchUUID),
			Limit:  10,
		})
		require.NoError(t, err)
		require.Equal(t, 2, result.Total)
		require.Len(t, result.Entries, 2)
		require.Equal(t, "request-id-match", result.Entries[0].ID)
		require.Equal(t, "session-id-match", result.Entries[1].ID)
	})
}

func TestSQLReaderGetLogs_SearchMatchesErrorMessage(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		ctx := context.Background()
		err = store.WriteBatch(ctx, []*LogEntry{
			{
				ID:             "timeout-match",
				Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				ErrorType:      "provider_error",
				Data: &LogData{
					ErrorMessage: `failed to send request: Post "https://api.openai.com/v1/chat/completions": http2: timeout awaiting response headers`,
				},
			},
			{
				ID:             "other-error",
				Timestamp:      time.Date(2026, 1, 16, 11, 0, 0, 0, time.UTC),
				RequestedModel: "gpt-5",
				Provider:       "openai",
				ErrorType:      "provider_error",
				Data: &LogData{
					ErrorMessage: "upstream refused connection",
				},
			},
		})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		result, err := reader.GetLogs(ctx, LogQueryParams{
			Search: "timeout awaiting response headers",
			Limit:  10,
		})
		require.NoError(t, err)
		require.Equal(t, 1, result.Total)
		require.Len(t, result.Entries, 1)
		require.Equal(t, "timeout-match", result.Entries[0].ID)
	})
}
