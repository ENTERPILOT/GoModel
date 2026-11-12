package usage

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// Cost bases a benchmark can measure.
const (
	BenchmarkCostBasisProvider = "provider"
	BenchmarkCostBasisBilled   = "billed"
)

const (
	// MaxBenchmarkMonths caps the months one benchmark covers, the current
	// one included, in line with MaxDateRangeDays.
	MaxBenchmarkMonths = 12
	// MaxBenchmarkTopUsers caps the per-user rows a benchmark month lists.
	MaxBenchmarkTopUsers = 50
)

// maxBenchmarkUserMonths bounds the (user, month) totals one benchmark holds,
// so memory follows the group's direct children, not the end-user paths
// streamed into it. Past it the benchmark fails rather than leaving users
// out. A variable so tests can lower it.
var maxBenchmarkUserMonths = 100000

// BenchmarkParams configures a per-user cost benchmark. StartDate and EndDate
// span whole months in TimeZone.
type BenchmarkParams struct {
	UsageQueryParams
	// Parent is the group: each direct child of it is one user, with its
	// whole subtree summed, like the invoice's customers. Usage recorded on
	// Parent itself belongs to no user. Empty means the root.
	Parent string
	// CostBasis is provider (default) or billed.
	CostBasis string
	// TopUsers lists that many of each month's highest-cost users; 0 lists
	// none, so the result holds aggregates only.
	TopUsers int
	// Now marks the month it falls in, in TimeZone, as partial.
	Now time.Time
}

// Benchmark is the cost per active user of a group's direct children, one
// entry per calendar month in TimeZone, oldest first.
type Benchmark struct {
	Parent    string           `json:"parent"`
	TimeZone  string           `json:"time_zone"`
	Currency  string           `json:"currency"`
	CostBasis string           `json:"cost_basis" enums:"provider,billed"`
	Months    []BenchmarkMonth `json:"months"`
}

// BenchmarkMonth holds one month's statistics. A user is active with at least
// one request in the month, response-cache hits included. Mean, P50, P90 and
// Max are nil without active users; percentiles interpolate linearly between
// closest ranks. A user's cost is what the invoice reports for it. Users whose
// month cost is wholly unknown (unpriced models) count as active at cost 0
// and are counted in UnpricedUsers; users with some unknown cost count at
// their known cost and are counted in PartlyPricedUsers.
type BenchmarkMonth struct {
	Month             string   `json:"month"`
	Partial           bool     `json:"partial"`
	ActiveUsers       int      `json:"active_users"`
	UnpricedUsers     int      `json:"unpriced_users"`
	PartlyPricedUsers int      `json:"partly_priced_users"`
	TotalCost         float64  `json:"total_cost"`
	Mean              *float64 `json:"mean" extensions:"x-nullable"`
	P50               *float64 `json:"p50" extensions:"x-nullable"`
	P90               *float64 `json:"p90" extensions:"x-nullable"`
	Max               *float64 `json:"max" extensions:"x-nullable"`
	// MeanChangePercent and P50ChangePercent compare with the previous month;
	// nil for the first month, the partial current month, a month without
	// active users, or a previous month whose value is not positive.
	MeanChangePercent *float64 `json:"mean_change_percent" extensions:"x-nullable"`
	P50ChangePercent  *float64 `json:"p50_change_percent" extensions:"x-nullable"`
	// UnattributedCost is usage recorded on the parent path itself, which no
	// user carries; it is not part of TotalCost.
	UnattributedCost float64 `json:"unattributed_cost"`
	// TopUsers is set only when requested.
	TopUsers []BenchmarkUser `json:"top_users,omitempty"`
}

// BenchmarkUser is one user's month. Requests counts provider-bound
// requests, like the invoice; cache hits are not included.
type BenchmarkUser struct {
	User     string  `json:"user"`
	Requests int     `json:"requests"`
	Cost     float64 `json:"cost"`
}

// NormalizeBenchmarkCostBasis returns the cost basis, provider when empty.
func NormalizeBenchmarkCostBasis(raw string) (string, error) {
	switch basis := strings.ToLower(strings.TrimSpace(raw)); basis {
	case "", BenchmarkCostBasisProvider:
		return BenchmarkCostBasisProvider, nil
	case BenchmarkCostBasisBilled:
		return basis, nil
	default:
		return "", core.NewInvalidRequestError("invalid cost_basis: use provider or billed", nil)
	}
}

// readUsageBenchmark streams the breakdown's (user path, month) groups,
// uncapped and without its top-N fold, sums them per direct child of the
// parent, and derives the statistics in Go so every backend shares them.
func readUsageBenchmark(ctx context.Context, params BenchmarkParams, stream breakdownGroupStream) (*Benchmark, error) {
	parent, err := core.NormalizeUserPath(params.Parent)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid parent: "+err.Error(), err)
	}
	params.Parent = cmp.Or(parent, "/")
	if params.CostBasis, err = NormalizeBenchmarkCostBasis(params.CostBasis); err != nil {
		return nil, err
	}
	params.TopUsers = min(max(params.TopUsers, 0), MaxBenchmarkTopUsers)
	// Uncached like the invoice: a cache hit's stored cost is the provider
	// cost it avoided. The hits are read separately below.
	params.CacheMode = CacheModeUncached
	params.Interval = "monthly"

	benchmark := EmptyBenchmark(params)
	// Refused before any query runs: the database aggregates every leaf
	// path of every month in range.
	if len(benchmark.Months) > MaxBenchmarkMonths {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("the benchmark covers at most %d months", MaxBenchmarkMonths), nil)
	}
	breakdown := UsageBreakdownParams{
		UsageQueryParams: params.UsageQueryParams,
		Rows:             BreakdownUserPath,
		Columns:          BreakdownPeriod,
		Metric:           BreakdownMetricCost,
	}
	switch {
	case breakdown.UserPath == "" || core.UserPathContains(breakdown.UserPath, params.Parent):
		breakdown.UserPath = params.Parent
	case !core.UserPathContains(params.Parent, breakdown.UserPath):
		return benchmark, nil
	}

	totals := benchmarkTotals{parent: params.Parent, users: map[benchmarkKey]*benchmarkUserMonth{}}
	if err := stream(ctx, breakdown, 0, totals.addProvider); err != nil {
		return nil, err
	}
	hits := breakdown
	hits.CacheMode = billedCacheHitsMode(breakdown.CacheMode)
	if err := stream(ctx, hits, 0, totals.addCacheHit); err != nil {
		return nil, err
	}
	totals.fill(benchmark, params)
	return benchmark, nil
}

// EmptyBenchmark returns params' months without usage.
func EmptyBenchmark(params BenchmarkParams) *Benchmark {
	benchmark := &Benchmark{
		Parent:    cmp.Or(params.Parent, "/"),
		TimeZone:  usageTimeZone(params.UsageQueryParams),
		Currency:  InvoiceCurrency,
		CostBasis: cmp.Or(params.CostBasis, BenchmarkCostBasisProvider),
		Months:    []BenchmarkMonth{},
	}
	if params.StartDate.IsZero() || params.EndDate.IsZero() {
		return benchmark
	}
	location := usageLocation(params.UsageQueryParams)
	current := ""
	if !params.Now.IsZero() {
		current = params.Now.In(location).Format("2006-01")
	}
	start := params.StartDate.In(location)
	end := params.EndDate.In(location)
	for month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, location); !month.After(end); month = month.AddDate(0, 1, 0) {
		key := month.Format("2006-01")
		benchmark.Months = append(benchmark.Months, BenchmarkMonth{Month: key, Partial: key == current})
	}
	return benchmark
}

// topBenchmarkUsers returns the limit highest-cost users, ties by path.
func topBenchmarkUsers(users []BenchmarkUser, limit int) []BenchmarkUser {
	sorted := slices.Clone(users)
	slices.SortFunc(sorted, func(a, b BenchmarkUser) int {
		return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(a.User, b.User))
	})
	sorted = sorted[:min(limit, len(sorted))]
	for i := range sorted {
		sorted[i].Cost = roundBenchmarkCost(sorted[i].Cost)
	}
	return sorted
}

// changePercent is the change from previous to current in percent, rounded
// to 1/10000 of a percent; nil without both values or a positive previous.
func changePercent(previous, current *float64) *float64 {
	if previous == nil || current == nil || *previous <= 0 {
		return nil
	}
	return new(math.Round((*current / *previous - 1)*100*1e4) / 1e4)
}

func roundBenchmarkCost(cost float64) float64 {
	return math.Round(cost*invoiceCostPrecision) / invoiceCostPrecision
}
