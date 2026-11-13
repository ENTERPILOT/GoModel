package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestReadersUsageBenchmarkAccuracy(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertBenchmarkAccuracy(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertBenchmarkAccuracy(t, store, reader)
		})
	})
}

// benchmarkAgreementFixture: /eng's children in March 2026, each pricing
// case the invoice distinguishes, plus a sibling path that only shares a
// prefix with one of them.
func benchmarkAgreementFixture(b *benchmarkEntries) []*UsageEntry {
	ts := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	hit := func(userPath string, cost, billed *float64) *UsageEntry {
		e := b.entry(ts, userPath, cost, billed)
		e.CacheType = CacheTypeExact
		return e
	}
	return []*UsageEntry{
		b.entry(ts, "/eng/alice/laptop", new(1.0), new(1.2)),
		b.entry(ts, "/eng/alice/ci", new(2.0), new(2.4)),
		b.entry(ts, "/eng/al/x", new(7.0), new(8.0)), // not part of /eng/alice
		hit("/eng/dave", new(9.0), new(0.5)),         // billed cache hit only
		b.entry(ts, "/eng/erin", nil, nil),           // unpriced ...
		hit("/eng/erin", nil, new(0.0)),              // ... plus an unbilled hit
		b.entry(ts, "/eng/frank", new(3.0), new(3.6)),
		b.entry(ts, "/eng/frank", nil, nil),  // partly priced within one path
		hit("/eng/gina", new(2.0), new(0.0)), // active through an unbilled hit
		b.entry(ts, "/eng/hana", new(1.0), new(1.2)),
		hit("/eng/hana", nil, nil), // ... plus a billed hit of unknown cost
		hit("/eng/ivan", nil, nil), // billed hits of unknown cost only
		b.entry(ts, "/eng", new(10.0), new(11.0)),
		b.entry(ts, "/sales/x", new(100.0), new(100.0)),
	}
}

func benchmarkMarch() UsageQueryParams {
	return UsageQueryParams{
		StartDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	}
}

func assertBenchmarkAccuracy(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	entries := &benchmarkEntries{}
	require.NoError(t, store.WriteBatch(ctx, benchmarkAgreementFixture(entries)))

	t.Run("per-user totals agree with the invoice", func(t *testing.T) {
		invoice, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: benchmarkMarch(), Parent: "/eng"})
		require.NoError(t, err)
		// The invoice bills hana's known cost and leaves ivan out; only the
		// billed benchmark can say that cache hits of unknown cost are missing.
		offInvoiceUnpriced := map[string]int{BenchmarkCostBasisProvider: 0, BenchmarkCostBasisBilled: 1}
		partly := map[string]int{BenchmarkCostBasisProvider: 1, BenchmarkCostBasisBilled: 2}
		for _, basis := range []string{BenchmarkCostBasisProvider, BenchmarkCostBasisBilled} {
			t.Run(basis, func(t *testing.T) {
				benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{
					UsageQueryParams: benchmarkMarch(), Parent: "/eng", CostBasis: basis, TopUsers: MaxBenchmarkTopUsers,
				})
				require.NoError(t, err)
				require.Len(t, benchmark.Months, 1)
				assertBenchmarkMatchesInvoice(t, benchmark.Months[0], invoice, "/eng", basis, offInvoiceUnpriced[basis])
				assert.Equal(t, partly[basis], benchmark.Months[0].PartlyPricedUsers)
			})
		}
	})

	tests := []struct {
		name   string
		params BenchmarkParams
		want   []benchmarkRow
		// wantPartly is each month's partly_priced_users.
		wantPartly []int
	}{
		{
			name:       "provider: unpriced, partly priced and a sibling path",
			params:     BenchmarkParams{UsageQueryParams: benchmarkMarch(), Parent: "/eng"},
			want:       []benchmarkRow{{"2026-03", false, 8, 1, 14, 10, new(1.75), new(0.5), new(4.2), new(7.0), nil, nil}},
			wantPartly: []int{1},
		},
		{
			name:       "billed: unknown costs are counted, also those of cache hits",
			params:     BenchmarkParams{UsageQueryParams: benchmarkMarch(), Parent: "/eng", CostBasis: BenchmarkCostBasisBilled},
			want:       []benchmarkRow{{"2026-03", false, 8, 2, 16.9, 11, new(2.1125), new(0.85), new(4.92), new(8.0), nil, nil}},
			wantPartly: []int{2},
		},
		{
			name:       "a parent does not absorb a sibling sharing its prefix",
			params:     BenchmarkParams{UsageQueryParams: benchmarkMarch(), Parent: "/eng/al"},
			want:       []benchmarkRow{{"2026-03", false, 1, 0, 7, 0, new(7.0), new(7.0), new(7.0), new(7.0), nil, nil}},
			wantPartly: []int{0},
		},
		{
			name:       "a scope equal to the parent keeps the unattributed cost",
			params:     BenchmarkParams{UsageQueryParams: withScope(benchmarkMarch(), "/eng"), Parent: "/eng"},
			want:       []benchmarkRow{{"2026-03", false, 8, 1, 14, 10, new(1.75), new(0.5), new(4.2), new(7.0), nil, nil}},
			wantPartly: []int{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			benchmark, err := reader.GetUsageBenchmark(ctx, tt.params)
			require.NoError(t, err)
			assert.Equal(t, tt.want, flattenBenchmark(benchmark))
			partly := make([]int, 0, len(benchmark.Months))
			for _, month := range benchmark.Months {
				partly = append(partly, month.PartlyPricedUsers)
			}
			assert.Equal(t, tt.wantPartly, partly)
		})
	}

	t.Run("statistics with ties and two users", func(t *testing.T) {
		jan := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		feb := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
		require.NoError(t, store.WriteBatch(ctx, []*UsageEntry{
			entries.entry(jan, "/ties/a", new(1.0), new(1.0)),
			entries.entry(jan, "/ties/b", new(2.0), new(2.0)),
			entries.entry(jan, "/ties/c", new(2.0), new(2.0)),
			entries.entry(jan, "/ties/d", new(10.0), new(10.0)),
			entries.entry(feb, "/ties/a", new(4.0), new(4.0)),
			entries.entry(feb, "/ties/b", new(4.0), new(4.0)),
		}))
		params := benchmarkQuarter()
		params.EndDate = time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
		benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: params, Parent: "/ties"})
		require.NoError(t, err)
		assert.Equal(t, []benchmarkRow{
			{"2026-01", false, 4, 0, 15, 0, new(3.75), new(2.0), new(7.6), new(10.0), nil, nil},
			{"2026-02", false, 2, 0, 8, 0, new(4.0), new(4.0), new(4.0), new(4.0), new(6.6667), new(100.0)},
		}, flattenBenchmark(benchmark))
	})

	t.Run("month edges", func(t *testing.T) {
		assertBenchmarkMonthEdges(t, store, reader, entries)
	})
}

// assertBenchmarkMatchesInvoice checks that month, read with top users,
// reports each invoice customer's subtotal on basis: a null invoice cost is
// an unpriced user at 0, and a user the invoice leaves out costs 0.
// offInvoiceUnpriced of those users are unpriced: the invoice has no line for
// cache hits of unknown billed cost.
func assertBenchmarkMatchesInvoice(t *testing.T, month BenchmarkMonth, invoice *Invoice, parent, basis string, offInvoiceUnpriced int) {
	t.Helper()
	users := map[string]BenchmarkUser{}
	for _, user := range month.TopUsers {
		users[user.User] = user
	}
	require.Len(t, users, month.ActiveUsers, "every active user is listed")
	unpriced, total := 0, 0.0
	for _, customer := range invoice.Customers {
		cost := customer.Subtotal.ProviderCost
		if basis == BenchmarkCostBasisBilled {
			cost = customer.Subtotal.BilledCost
		}
		if customer.Customer == parent {
			require.NotNil(t, cost)
			assert.InDelta(t, *cost, month.UnattributedCost, 1e-9, "unattributed")
			continue
		}
		user, ok := users[customer.Customer]
		require.True(t, ok, "%s is on the invoice but not in the benchmark", customer.Customer)
		delete(users, customer.Customer)
		assert.Equal(t, customer.Subtotal.Requests, user.Requests, "%s requests", customer.Customer)
		if cost == nil {
			unpriced++
			assert.Zero(t, user.Cost, "%s is unpriced", customer.Customer)
			continue
		}
		total += *cost
		assert.InDelta(t, *cost, user.Cost, 1e-9, "%s cost", customer.Customer)
	}
	for name, user := range users {
		assert.Zero(t, user.Cost, "%s is not billed on the invoice", name)
		assert.Zero(t, user.Requests, "%s has no provider requests", name)
	}
	assert.Equal(t, unpriced+offInvoiceUnpriced, month.UnpricedUsers)
	assert.InDelta(t, total, month.TotalCost, 1e-9)
}

// assertBenchmarkMonthEdges puts usage on the first and last instant of
// months, in Warsaw across both DST changes and in UTC; user i of a group
// costs 2^i, so each month's total names the instants it holds.
func assertBenchmarkMonthEdges(t *testing.T, store usageBatchWriter, reader UsageReader, entries *benchmarkEntries) {
	t.Helper()
	ctx := context.Background()
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	instant := func(year int, month time.Month, day, hour, minute, second, ms int) time.Time {
		return time.Date(year, month, day, hour, minute, second, ms*int(time.Millisecond), time.UTC)
	}
	edges := map[string][]time.Time{
		"/dst": {
			instant(2026, 2, 28, 22, 59, 59, 999),  // last of February (CET)
			instant(2026, 2, 28, 23, 0, 0, 0),      // first of March (CET)
			instant(2026, 3, 31, 21, 59, 59, 999),  // last of March (CEST, after the change)
			instant(2026, 3, 31, 22, 0, 0, 0),      // first of April
			instant(2026, 9, 30, 21, 59, 59, 999),  // last of September
			instant(2026, 9, 30, 22, 0, 0, 0),      // first of October (CEST)
			instant(2026, 10, 31, 22, 59, 59, 999), // last of October (CET, after the change)
			instant(2026, 10, 31, 23, 0, 0, 0),     // first of November
		},
		"/utc": {
			instant(2026, 2, 28, 23, 59, 59, 999),
			instant(2026, 3, 1, 0, 0, 0, 0),
			instant(2026, 3, 31, 23, 59, 59, 999),
			instant(2026, 4, 1, 0, 0, 0, 0),
		},
	}
	var rows []*UsageEntry
	for group, instants := range edges {
		for i, ts := range instants {
			cost := float64(int(1) << i)
			rows = append(rows, entries.entry(ts, group+"/u", new(cost), new(cost)))
		}
	}
	require.NoError(t, store.WriteBatch(ctx, rows))

	tests := []struct {
		name   string
		group  string
		params UsageQueryParams
		want   map[string]float64
	}{
		{
			name:  "Europe/Warsaw across both DST changes",
			group: "/dst",
			params: UsageQueryParams{
				StartDate: time.Date(2026, 2, 1, 0, 0, 0, 0, warsaw),
				EndDate:   time.Date(2026, 11, 30, 0, 0, 0, 0, warsaw),
				TimeZone:  warsaw.String(),
			},
			want: map[string]float64{"2026-02": 1, "2026-03": 2 + 4, "2026-04": 8, "2026-09": 16, "2026-10": 32 + 64, "2026-11": 128},
		},
		{
			name:  "UTC",
			group: "/utc",
			params: UsageQueryParams{
				StartDate: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
				EndDate:   time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
				TimeZone:  "UTC",
			},
			want: map[string]float64{"2026-02": 1, "2026-03": 2 + 4, "2026-04": 8},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			benchmark, err := reader.GetUsageBenchmark(ctx, BenchmarkParams{UsageQueryParams: tt.params, Parent: tt.group})
			require.NoError(t, err)
			got := map[string]float64{}
			for _, month := range benchmark.Months {
				if month.ActiveUsers > 0 {
					got[month.Month] = month.TotalCost
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
