package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBenchmarkChangeSkipsThePartialMonth(t *testing.T) {
	tests := []struct {
		name       string
		now        time.Time
		wantChange *float64
	}{
		{name: "complete month compares", now: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), wantChange: new(-50.0)},
		{name: "partial month does not", now: time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := BenchmarkParams{
				StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EndDate:   time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
				Parent:    "/eng", CostBasis: BenchmarkCostBasisProvider, Now: tt.now,
			}
			benchmark := EmptyBenchmark(params)
			totals := benchmarkTotals{parent: "/eng", users: map[benchmarkKey]*benchmarkUserMonth{}}
			for _, group := range []breakdownGroup{
				{Row: "/eng/a", Column: "2026-01", Requests: 1, PricedRequests: 1, TotalCost: new(10.0)},
				{Row: "/eng/a", Column: "2026-02", Requests: 1, PricedRequests: 1, TotalCost: new(5.0)},
			} {
				require.NoError(t, totals.addProvider(group))
			}
			totals.fill(benchmark, params)
			require.Len(t, benchmark.Months, 2)
			assert.Equal(t, tt.wantChange, benchmark.Months[1].MeanChangePercent)
			assert.Equal(t, tt.wantChange, benchmark.Months[1].P50ChangePercent)
			assert.Equal(t, new(5.0), benchmark.Months[1].Mean, "the statistics stay")
		})
	}
}

func TestBenchmarkRangeIsRefusedBeforeQuerying(t *testing.T) {
	tests := []struct {
		name      string
		start     time.Time
		wantErr   bool
		wantCalls int
	}{
		{name: "exactly the maximum", start: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), wantCalls: 2},
		{name: "one month over", start: time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			stream := func(context.Context, UsageBreakdownParams, int, func(breakdownGroup) error) error {
				calls++
				return nil
			}
			params := BenchmarkParams{
				StartDate: tt.start,
				EndDate:   time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			}
			benchmark, err := readUsageBenchmark(context.Background(), params, stream)
			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "at most 12 months")
				return
			}
			require.NoError(t, err)
			assert.Len(t, benchmark.Months, MaxBenchmarkMonths)
		})
	}
}

func TestBenchmarkCacheHitsOfUnknownBilledCost(t *testing.T) {
	provider := breakdownGroup{Row: "/eng/a", Column: "2026-01", Requests: 1, PricedRequests: 1, BilledPricedRequests: 1, TotalCost: new(1.0), BilledCost: new(1.2)}
	unknownHit := breakdownGroup{Row: "/eng/a", Column: "2026-01", Requests: 1}
	unbilledHit := breakdownGroup{Row: "/eng/a", Column: "2026-01", Requests: 1, PricedRequests: 1, BilledPricedRequests: 1, TotalCost: new(2.0), BilledCost: new(0.0)}
	tests := []struct {
		name         string
		provider     []breakdownGroup
		hits         []breakdownGroup
		basis        string
		wantCost     float64
		wantUnpriced int
		wantPartly   int
	}{
		{name: "billed: a hit of unknown cost beside known usage is partly priced", provider: []breakdownGroup{provider}, hits: []breakdownGroup{unknownHit}, basis: BenchmarkCostBasisBilled, wantCost: 1.2, wantPartly: 1},
		{name: "billed: only hits of unknown cost are unpriced", hits: []breakdownGroup{unknownHit}, basis: BenchmarkCostBasisBilled, wantUnpriced: 1},
		{name: "billed: an unbilled hit is a known zero", hits: []breakdownGroup{unbilledHit}, basis: BenchmarkCostBasisBilled},
		{name: "billed: an unbilled hit beside known usage stays priced", provider: []breakdownGroup{provider}, hits: []breakdownGroup{unbilledHit}, basis: BenchmarkCostBasisBilled, wantCost: 1.2},
		{name: "provider: a hit costs the provider nothing, known or not", provider: []breakdownGroup{provider}, hits: []breakdownGroup{unknownHit}, basis: BenchmarkCostBasisProvider, wantCost: 1},
		{name: "provider: only hits of unknown cost are a known zero", hits: []breakdownGroup{unknownHit}, basis: BenchmarkCostBasisProvider},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := BenchmarkParams{
				StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EndDate:   time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC),
				Parent:    "/eng", CostBasis: tt.basis,
			}
			benchmark := EmptyBenchmark(params)
			totals := benchmarkTotals{parent: "/eng", users: map[benchmarkKey]*benchmarkUserMonth{}}
			for _, group := range tt.provider {
				require.NoError(t, totals.addProvider(group))
			}
			for _, group := range tt.hits {
				require.NoError(t, totals.addCacheHit(group))
			}
			totals.fill(benchmark, params)
			require.Len(t, benchmark.Months, 1)
			month := benchmark.Months[0]
			assert.Equal(t, 1, month.ActiveUsers)
			assert.Equal(t, tt.wantCost, month.TotalCost)
			assert.Equal(t, tt.wantUnpriced, month.UnpricedUsers, "unpriced")
			assert.Equal(t, tt.wantPartly, month.PartlyPricedUsers, "partly priced")
		})
	}
}
