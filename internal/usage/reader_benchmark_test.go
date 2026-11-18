package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// benchmarkEntries builds usage rows with distinct ids.
type benchmarkEntries struct{ n int }

func (b *benchmarkEntries) entry(ts time.Time, userPath string, cost, billed *float64) *UsageEntry {
	b.n++
	return &UsageEntry{
		ID: fmt.Sprintf("7d2a0000-0000-4000-8000-%012d", b.n), RequestID: fmt.Sprintf("req-bm-%d", b.n), ProviderID: fmt.Sprintf("p-bm-%d", b.n),
		Timestamp: ts, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", UserPath: userPath,
		InputTokens: 10, OutputTokens: 1, TotalTokens: 11, TotalCost: cost, BilledCost: billed,
	}
}

// benchmarkFixture: /eng's children in January and March 2026 (none in
// February), usage on /eng itself, and usage outside /eng.
func benchmarkFixture(b *benchmarkEntries) []*UsageEntry {
	jan := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	hit := b.entry(mar, "/eng/dave", new(9.0), new(0.5))
	hit.CacheType = CacheTypeExact
	return []*UsageEntry{
		b.entry(jan, "/eng/alice/laptop", new(1.0), new(1.2)),
		b.entry(jan, "/eng/alice/ci", new(2.0), new(2.4)),
		b.entry(jan, "/eng/bob", new(5.0), new(6.0)),
		b.entry(jan, "/eng", new(10.0), new(10.0)),
		b.entry(jan, "/sales/carol", new(100.0), new(100.0)),
		b.entry(mar, "/eng/alice", new(4.0), new(5.0)),
		hit,                                 // active through a cache hit only
		b.entry(mar, "/eng/erin", nil, nil), // unpriced
	}
}

func TestReadersUsageBenchmark(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertBenchmarkReads(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertBenchmarkReads(t, store, reader)
		})
	})
}

// benchmarkRow flattens a month's statistics.
type benchmarkRow struct {
	Month                 string
	Partial               bool
	Active, Unpriced      int
	Total, Unattributed   float64
	Mean, P50, P90, Max   *float64
	MeanChange, P50Change *float64
}

func flattenBenchmark(benchmark *Benchmark) []benchmarkRow {
	rows := make([]benchmarkRow, 0, len(benchmark.Months))
	for _, m := range benchmark.Months {
		rows = append(rows, benchmarkRow{
			m.Month, m.Partial, m.ActiveUsers, m.UnpricedUsers, m.TotalCost, m.UnattributedCost,
			m.Mean, m.P50, m.P90, m.Max, m.MeanChangePercent, m.P50ChangePercent,
		})
	}
	return rows
}

func benchmarkQuarter() UsageQueryParams {
	return UsageQueryParams{
		StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	}
}

func assertBenchmarkReads(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	entries := &benchmarkEntries{}
	require.NoError(t, store.WriteBatch(ctx, benchmarkFixture(entries)))
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	february := benchmarkRow{Month: "2026-02"}

	tests := []struct {
		name   string
		params BenchmarkParams
		want   []benchmarkRow
	}{
		{
			name:   "provider cost per direct child, subtrees summed",
			params: BenchmarkParams{UsageQueryParams: benchmarkQuarter(), Parent: "/eng", Now: now},
			want: []benchmarkRow{
				{"2026-01", false, 2, 0, 8, 10, new(4.0), new(4.0), new(4.8), new(5.0), nil, nil},
				february,
				// dave's cache hit costs the provider nothing; erin is unpriced.
				{"2026-03", true, 3, 1, 4, 0, new(1.333333333), new(0.0), new(3.2), new(4.0), nil, nil},
			},
		},
		{
			name:   "billed cost adds markup and billed cache hits",
			params: BenchmarkParams{UsageQueryParams: benchmarkQuarter(), Parent: "/eng/", CostBasis: BenchmarkCostBasisBilled, Now: now},
			want: []benchmarkRow{
				{"2026-01", false, 2, 0, 9.6, 10, new(4.8), new(4.8), new(5.76), new(6.0), nil, nil},
				february,
				{"2026-03", true, 3, 1, 5.5, 0, new(1.833333333), new(0.5), new(4.1), new(5.0), nil, nil},
			},
		},
		{
			name:   "the root's children are the top-level groups",
			params: BenchmarkParams{UsageQueryParams: withInterval(benchmarkQuarter(), "daily"), Now: now},
			want: []benchmarkRow{
				{"2026-01", false, 2, 0, 118, 0, new(59.0), new(59.0), new(91.8), new(100.0), nil, nil},
				february,
				// erin's unknown cost is part of /eng's known total.
				{"2026-03", true, 1, 0, 4, 0, new(4.0), new(4.0), new(4.0), new(4.0), nil, nil},
			},
		},
		{
			name:   "a filter narrows the rows",
			params: BenchmarkParams{UsageQueryParams: withUserPaths(benchmarkQuarter(), "/eng/bob"), Parent: "/eng"},
			want: []benchmarkRow{
				{"2026-01", false, 1, 0, 5, 0, new(5.0), new(5.0), new(5.0), new(5.0), nil, nil},
				february,
				{Month: "2026-03"},
			},
		},
		{
			name:   "a scope outside the parent benchmarks nobody",
			params: BenchmarkParams{UsageQueryParams: withScope(benchmarkQuarter(), "/sales"), Parent: "/eng"},
			want:   []benchmarkRow{{Month: "2026-01"}, february, {Month: "2026-03"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			benchmark, err := reader.GetUsageBenchmark(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, benchmark)
			assert.Equal(t, InvoiceCurrency, benchmark.Currency)
			assert.Equal(t, tt.want, flattenBenchmark(benchmark))
			for _, month := range benchmark.Months {
				assert.Nil(t, month.TopUsers, "aggregates only unless asked")
			}
		})
	}

	t.Run("month-over-month change", func(t *testing.T) {
		params := benchmarkQuarter()
		params.EndDate = time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		params.StartDate = time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
		dec := time.Date(2025, 12, 3, 0, 0, 0, 0, time.UTC)
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			entries.entry(dec, "/eng/alice", new(2.0), new(2.0)),
			entries.entry(dec, "/eng/bob", new(3.0), new(3.0)),
		}))
		benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: params, Parent: "/eng"})
		require.NoError(t, err)
		require.Len(t, benchmark.Months, 2)
		assert.Equal(t, "2025-12", benchmark.Months[0].Month)
		assert.Equal(t, new(60.0), benchmark.Months[1].MeanChangePercent, "2.5 to 4")
		assert.Equal(t, new(60.0), benchmark.Months[1].P50ChangePercent)
	})

	t.Run("top users only when asked", func(t *testing.T) {
		benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: benchmarkQuarter(), Parent: "/eng", TopUsers: 1})
		require.NoError(t, err)
		require.Len(t, benchmark.Months, 3)
		assert.Equal(t, []BenchmarkUser{{User: "/eng/bob", Requests: 1, Cost: 5}}, benchmark.Months[0].TopUsers)
		assert.Empty(t, benchmark.Months[1].TopUsers)
	})

	t.Run("months follow the time zone", func(t *testing.T) {
		// 2026-01-31 23:30 UTC is February 1 in Warsaw.
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			entries.entry(time.Date(2026, 1, 31, 23, 30, 0, 0, time.UTC), "/tz/u", new(1.0), new(1.0)),
		}))
		warsaw, err := time.LoadLocation("Europe/Warsaw")
		require.NoError(t, err)
		local := UsageQueryParams{
			StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, warsaw),
			EndDate:   time.Date(2026, 2, 28, 0, 0, 0, 0, warsaw),
			TimeZone:  warsaw.String(),
		}
		utc := benchmarkQuarter()
		utc.EndDate = time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
		for _, tc := range []struct {
			params     UsageQueryParams
			wantActive []int
		}{
			{local, []int{0, 1}},
			{utc, []int{1, 0}},
		} {
			benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: tc.params, Parent: "/tz"})
			require.NoError(t, err)
			require.Len(t, benchmark.Months, 2)
			assert.Equal(t, tc.params.TimeZone, benchmark.TimeZone)
			assert.Equal(t, tc.wantActive, []int{benchmark.Months[0].ActiveUsers, benchmark.Months[1].ActiveUsers}, tc.params.TimeZone)
		}
	})

	t.Run("invalid parent", func(t *testing.T) {
		_, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: benchmarkQuarter(), Parent: "/a/../b"})
		require.Error(t, err)
	})
}

func TestReadersUsageBenchmarkCountsEveryUser(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertBenchmarkCountsEveryUser(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertBenchmarkCountsEveryUser(t, store, reader)
		})
	})
}

func assertBenchmarkCountsEveryUser(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	// 60 users, well past the pivot's 50-key limit, each on two leaf paths:
	// user i costs i in total.
	entries := &benchmarkEntries{}
	ts := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	var rows []*UsageEntry
	for i := 1; i <= 60; i++ {
		half := float64(i) / 2
		rows = append(rows,
			entries.entry(ts, fmt.Sprintf("/many/u-%02d/laptop", i), new(half), new(half)),
			entries.entry(ts, fmt.Sprintf("/many/u-%02d/ci", i), new(half), new(half)),
		)
	}
	require.NoError(t, store.WriteBatch(ctx, rows))
	params := benchmarkQuarter()
	params.EndDate = time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	// Far fewer groups allowed than the 120 the fixture has.
	setPackageInt(t, &maxBreakdownGroups, 5)

	t.Run("all users are counted", func(t *testing.T) {
		benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: params, Parent: "/many"})
		require.NoError(t, err)
		assert.Equal(t, []benchmarkRow{
			{"2026-01", false, 60, 0, 1830, 0, new(30.5), new(30.5), new(54.1), new(60.0), nil, nil},
		}, flattenBenchmark(benchmark))
	})

	t.Run("too many user months fail instead of truncating", func(t *testing.T) {
		setPackageInt(t, &maxBenchmarkUserMonths, 59)
		_, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: params, Parent: "/many"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "benchmark has more than 59 user months")
	})
}
