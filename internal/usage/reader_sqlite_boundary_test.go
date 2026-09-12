package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteReaderSummary_IncludesFractionalStartBoundaryAndExcludesFractionalEndBoundary(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "start-boundary",
			RequestID:    "req-start",
			ProviderID:   "provider-start",
			Timestamp:    time.Date(2026, 1, 15, 23, 0, 0, 123_000_000, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  10,
			OutputTokens: 10,
		},
		{
			ID:           "inside-range",
			RequestID:    "req-inside",
			ProviderID:   "provider-inside",
			Timestamp:    time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  20,
			OutputTokens: 20,
		},
		{
			ID:           "after-end-boundary",
			RequestID:    "req-after",
			ProviderID:   "provider-after",
			Timestamp:    time.Date(2026, 1, 16, 23, 0, 0, 123_000_000, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  999,
			OutputTokens: 999,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		TimeZone:  "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.Equal(t, 2, summary.TotalRequests)
	require.Equal(t, int64(30), summary.TotalTokens)
}

func TestSQLiteReaderGetDailyUsage_GroupsAcrossDSTTransitionInConfiguredTimeZone(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "before-dst-switch",
			RequestID:    "req-before",
			ProviderID:   "provider-before",
			Timestamp:    time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  10,
			OutputTokens: 10,
		},
		{
			ID:           "after-dst-switch",
			RequestID:    "req-after",
			ProviderID:   "provider-after",
			Timestamp:    time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  20,
			OutputTokens: 20,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 3, 29, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, location),
		Interval:  "daily",
		TimeZone:  "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.Len(t, daily, 1)
	require.Equal(t, "2026-03-29", daily[0].Date)
	require.Equal(t, 2, daily[0].Requests)
	require.Equal(t, int64(30), daily[0].TotalTokens)
}

func TestSQLiteReaderSummary_IncludesSpaceSeparatedBoundaryTimestamp(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()
	_, err = NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"space-boundary",
		"req-space",
		"provider-space",
		"2026-01-15 23:00:00+00:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		10,
		10,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		TimeZone:  "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.TotalRequests)
	require.Equal(t, int64(10), summary.TotalTokens)
}

func TestSQLiteReaderSummary_ExcludesLegacyOffsetTimestampBeforeUTCBoundary(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()
	_, err = NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"before-utc-boundary",
		"req-before",
		"provider-before",
		"2026-01-16 00:30:00+02:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		10,
		10,
		0.0,
		0.0,
		0.0,
		"",
		"inside-range",
		"req-inside",
		"provider-inside",
		"2026-01-16T12:00:00Z",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		20,
		20,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		TimeZone:  "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.TotalRequests)
	require.Equal(t, int64(20), summary.TotalTokens)
}

func TestSQLiteReaderGroupingRange_UsesAbsoluteTimestampExtremaAcrossOffsets(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()
	_, err = NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"earliest-positive-offset",
		"req-early",
		"provider-early",
		"2026-03-29 00:30:00+02:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		10,
		10,
		0.0,
		0.0,
		0.0,
		"",
		"middle-zulu",
		"req-middle",
		"provider-middle",
		"2026-03-28T23:00:00Z",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		15,
		15,
		0.0,
		0.0,
		0.0,
		"",
		"latest-negative-offset",
		"req-late",
		"provider-late",
		"2026-03-29 23:30:00-02:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		20,
		20,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	start, end, ok, err := reader.sqliteGroupingRange(ctx, UsageQueryParams{
		TimeZone: "Europe/Warsaw",
	})
	require.NoError(t, err)
	require.True(t, ok)

	expectedStart := time.Date(2026, 3, 28, 22, 30, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 3, 30, 1, 30, 1, 0, time.UTC)
	require.True(t, start.Equal(expectedStart), "expected range start %s, got %s", expectedStart, start)
	require.True(t, end.Equal(expectedEnd), "expected range end %s, got %s", expectedEnd, end)
}

func TestSQLiteReaderGetUsageLog_OrdersMixedTimestampFormatsByAbsoluteTime(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()
	_, err = NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"latest-negative-offset",
		"req-latest",
		"provider-latest",
		"2026-03-29 23:30:00-02:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		30,
		30,
		0.0,
		0.0,
		0.0,
		"",
		"middle-zulu",
		"req-middle",
		"provider-middle",
		"2026-03-29T23:00:00Z",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		20,
		20,
		0.0,
		0.0,
		0.0,
		"",
		"earliest-positive-offset",
		"req-earliest",
		"provider-earliest",
		"2026-03-29 00:30:00+02:00",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		10,
		10,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	log, err := reader.GetUsageLog(ctx, UsageLogParams{
		Limit:  2,
		Offset: 0,
	})
	require.NoError(t, err)
	require.Len(t, log.Entries, 2)
	require.Equal(t, "latest-negative-offset", log.Entries[0].ID)
	require.Equal(t, "middle-zulu", log.Entries[1].ID)
}

func TestSQLiteReaderGetUsageByModel_CollapsesBlankProviderNameIntoProviderGroup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "usage-1",
			RequestID:    "req-1",
			ProviderID:   "provider-1",
			Timestamp:    time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			ProviderName: "",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  10,
			OutputTokens: 20,
		},
		{
			ID:           "usage-2",
			RequestID:    "req-2",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 4, 7, 10, 1, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			ProviderName: " openai ",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  30,
			OutputTokens: 40,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	got, err := reader.GetUsageByModel(ctx, UsageQueryParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "openai", got[0].ProviderName)
	require.Equal(t, int64(40), got[0].InputTokens)
	require.Equal(t, int64(60), got[0].OutputTokens)
}

func TestSQLiteReaderGetUsageByUserPath_GroupsByTrackedPath(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "usage-root-blank",
			RequestID:    "req-root-blank",
			ProviderID:   "provider-root-blank",
			Timestamp:    time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			UserPath:     "",
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
		{
			ID:           "usage-root-explicit",
			RequestID:    "req-root-explicit",
			ProviderID:   "provider-root-explicit",
			Timestamp:    time.Date(2026, 4, 7, 10, 1, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			UserPath:     "/",
			InputTokens:  5,
			OutputTokens: 15,
			TotalTokens:  20,
		},
		{
			ID:           "usage-alpha",
			RequestID:    "req-alpha",
			ProviderID:   "provider-alpha",
			Timestamp:    time.Date(2026, 4, 7, 10, 2, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			UserPath:     "/team/alpha",
			InputTokens:  30,
			OutputTokens: 40,
			TotalTokens:  70,
		},
		{
			ID:           "usage-beta",
			RequestID:    "req-beta",
			ProviderID:   "provider-beta",
			Timestamp:    time.Date(2026, 4, 7, 10, 3, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			UserPath:     "/team/beta",
			InputTokens:  50,
			OutputTokens: 60,
			TotalTokens:  110,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	got, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{})
	require.NoError(t, err)

	byPath := make(map[string]UserPathUsage, len(got))
	for _, row := range got {
		byPath[row.UserPath] = row
	}
	require.Len(t, byPath, 3)
	require.Equal(t, int64(15), byPath["/"].InputTokens)
	require.Equal(t, int64(50), byPath["/"].TotalTokens)
	require.Equal(t, int64(70), byPath["/team/alpha"].TotalTokens)
	require.Equal(t, int64(60), byPath["/team/beta"].OutputTokens)

	filtered, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{UserPath: "/team"})
	require.NoError(t, err)

	filteredByPath := make(map[string]UserPathUsage, len(filtered))
	for _, row := range filtered {
		filteredByPath[row.UserPath] = row
	}
	require.Len(t, filteredByPath, 2)
	_, ok := filteredByPath["/"]
	require.False(t, ok)

	rootFiltered, err := reader.GetUsageByUserPath(ctx, UsageQueryParams{UserPath: "/"})
	require.NoError(t, err)

	rootFilteredByPath := make(map[string]UserPathUsage, len(rootFiltered))
	for _, row := range rootFiltered {
		rootFilteredByPath[row.UserPath] = row
	}
	require.Len(t, rootFilteredByPath, len(byPath), "root filter must include every grouped usage row: %#v", rootFiltered)
	require.Equal(t, int64(50), rootFilteredByPath["/"].TotalTokens)
	require.Equal(t, int64(70), rootFilteredByPath["/team/alpha"].TotalTokens)
	require.Equal(t, int64(110), rootFilteredByPath["/team/beta"].TotalTokens)
}

func TestSQLiteStoreCleanup_KeepsNewerLegacyOffsetRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()
	db.SetMaxOpenConns(1)

	store, err := NewSQLiteStore(db, 1)
	require.NoError(t, err)

	defer store.Close()

	cutoff := time.Now().AddDate(0, 0, -1).UTC().Truncate(time.Second)
	keepTimestamp := cutoff.Add(90 * time.Minute).In(time.FixedZone("minus2", -2*60*60)).Format("2006-01-02 15:04:05-07:00")
	deleteTimestamp := cutoff.Add(-90 * time.Minute).In(time.FixedZone("plus2", 2*60*60)).Format("2006-01-02 15:04:05-07:00")

	_, err = db.Exec(`
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"keep-newer-legacy",
		"req-keep",
		"provider-keep",
		keepTimestamp,
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		10,
		10,
		0.0,
		0.0,
		0.0,
		"",
		"delete-older-legacy",
		"req-delete",
		"provider-delete",
		deleteTimestamp,
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		0,
		20,
		20,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	store.cleanup()

	var remainingIDs []string
	rows, err := db.Query(`SELECT id FROM usage ORDER BY id`)
	require.NoError(t, err)

	defer rows.Close()

	for rows.Next() {
		var id string
		err := rows.Scan(&id)
		require.NoError(t, err)

		remainingIDs = append(remainingIDs, id)
	}
	err = rows.Err()
	require.NoError(t, err)
	require.Len(t, remainingIDs, 1)
	require.Equal(t, "keep-newer-legacy", remainingIDs[0])
}

func TestSQLiteReader_GetUsageLogFiltersByUserPathSubtree(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	defer store.Close()

	_, err = db.Exec(`
		INSERT INTO usage (
			id, request_id, provider_id, timestamp, model, provider, endpoint, user_path,
			input_tokens, output_tokens, total_tokens,
			input_cost, output_cost, total_cost, costs_calculation_caveat
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"match-team",
		"req-match",
		"provider-match",
		"2026-03-30T10:00:00Z",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		"/team/a",
		10,
		5,
		15,
		0.0,
		0.0,
		0.0,
		"",
		"miss-other",
		"req-miss",
		"provider-miss",
		"2026-03-30T11:00:00Z",
		"gpt-5",
		"openai",
		"/v1/chat/completions",
		"/other",
		10,
		5,
		15,
		0.0,
		0.0,
		0.0,
		"",
	)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	log, err := reader.GetUsageLog(ctx, UsageLogParams{
		UserPath: "/team",
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, log.Entries, 1)
	require.Equal(t, "match-team", log.Entries[0].ID)
}
