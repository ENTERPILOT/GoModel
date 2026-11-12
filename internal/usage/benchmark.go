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
	// MaxBenchmarkMonths caps the months one benchmark covers.
	MaxBenchmarkMonths = 24
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
// closest ranks. Users whose month cost is unknown (unpriced models) count as
// active at cost 0 and are counted in UnpricedUsers.
type BenchmarkMonth struct {
	Month         string   `json:"month"`
	Partial       bool     `json:"partial"`
	ActiveUsers   int      `json:"active_users"`
	UnpricedUsers int      `json:"unpriced_users"`
	TotalCost     float64  `json:"total_cost"`
	Mean          *float64 `json:"mean" extensions:"x-nullable"`
	P50           *float64 `json:"p50" extensions:"x-nullable"`
	P90           *float64 `json:"p90" extensions:"x-nullable"`
	Max           *float64 `json:"max" extensions:"x-nullable"`
	// MeanChangePercent and P50ChangePercent compare with the previous month;
	// nil for the first month, a month without active users, or a previous
	// month whose value is not positive.
	MeanChangePercent *float64 `json:"mean_change_percent" extensions:"x-nullable"`
	P50ChangePercent  *float64 `json:"p50_change_percent" extensions:"x-nullable"`
	// UnattributedCost is usage recorded on the parent path itself, which no
	// user carries; it is not part of TotalCost.
	UnattributedCost float64 `json:"unattributed_cost"`
	// TopUsers is set only when requested.
	TopUsers []BenchmarkUser `json:"top_users,omitempty"`
}

// BenchmarkUser is one user's month.
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
	if err := stream(ctx, breakdown, 0, totals.add(false)); err != nil {
		return nil, err
	}
	hits := breakdown
	hits.CacheMode = CacheModeCached
	if err := stream(ctx, hits, 0, totals.add(true)); err != nil {
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

type benchmarkKey struct{ month, user string }

type benchmarkUserMonth struct {
	providerRequests int
	requests         int
	providerCost     *float64
	billedCost       *float64
}

// benchmarkTotals sums storage groups into (month, user) totals; usage on
// the parent itself is kept under the parent's own key.
type benchmarkTotals struct {
	parent string
	users  map[benchmarkKey]*benchmarkUserMonth
}

func (b benchmarkTotals) add(cacheHits bool) func(breakdownGroup) error {
	return func(group breakdownGroup) error {
		key := benchmarkKey{month: group.Column, user: invoiceCustomer(b.parent, group.Row)}
		totals, ok := b.users[key]
		if !ok {
			if len(b.users) >= maxBenchmarkUserMonths {
				return core.NewInvalidRequestError(fmt.Sprintf(
					"benchmark has more than %d user months; pick a deeper parent, fewer months or narrower filters", maxBenchmarkUserMonths), nil)
			}
			totals = &benchmarkUserMonth{}
			b.users[key] = totals
		}
		totals.requests += group.Requests
		totals.billedCost = addCost(totals.billedCost, group.BilledCost)
		if !cacheHits {
			totals.providerRequests += group.Requests
			totals.providerCost = addCost(totals.providerCost, group.TotalCost)
		}
		return nil
	}
}

// fill computes each month's statistics into benchmark.
func (b benchmarkTotals) fill(benchmark *Benchmark, params BenchmarkParams) {
	users := map[string][]BenchmarkUser{}
	unattributed := map[string]float64{}
	unpriced := map[string]int{}
	for key, totals := range b.users {
		if totals.requests == 0 {
			continue
		}
		cost, known := totals.billedCost, totals.billedCost != nil
		if params.CostBasis == BenchmarkCostBasisProvider {
			cost, known = totals.providerCost, totals.providerCost != nil || totals.providerRequests == 0
		}
		value := 0.0
		if cost != nil {
			value = *cost
		}
		if key.user == b.parent {
			unattributed[key.month] += value
			continue
		}
		if !known {
			unpriced[key.month]++
		}
		users[key.month] = append(users[key.month], BenchmarkUser{User: key.user, Requests: totals.requests, Cost: value})
	}

	var previous *BenchmarkMonth
	for i := range benchmark.Months {
		month := &benchmark.Months[i]
		month.UnattributedCost = roundBenchmarkCost(unattributed[month.Month])
		month.UnpricedUsers = unpriced[month.Month]
		active := users[month.Month]
		month.ActiveUsers = len(active)
		costs := make([]float64, len(active))
		for j, user := range active {
			costs[j] = user.Cost
			month.TotalCost += user.Cost
		}
		month.TotalCost = roundBenchmarkCost(month.TotalCost)
		if stats, ok := computeCostStats(costs); ok {
			month.Mean = new(roundBenchmarkCost(stats.Mean))
			month.P50 = new(roundBenchmarkCost(stats.P50))
			month.P90 = new(roundBenchmarkCost(stats.P90))
			month.Max = new(roundBenchmarkCost(stats.Max))
		}
		if previous != nil {
			month.MeanChangePercent = changePercent(previous.Mean, month.Mean)
			month.P50ChangePercent = changePercent(previous.P50, month.P50)
		}
		if params.TopUsers > 0 {
			month.TopUsers = topBenchmarkUsers(active, params.TopUsers)
		}
		previous = month
	}
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
