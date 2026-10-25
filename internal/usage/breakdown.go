package usage

import (
	"cmp"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// BreakdownDimension names a field usage can be broken down by.
type BreakdownDimension string

// Dimensions accepted by GetUsageBreakdown.
const (
	BreakdownNone     BreakdownDimension = ""
	BreakdownModel    BreakdownDimension = "model"
	BreakdownProvider BreakdownDimension = "provider"
	BreakdownUserPath BreakdownDimension = "user_path"
	BreakdownLabel    BreakdownDimension = "label"
	BreakdownEndpoint BreakdownDimension = "endpoint"
	BreakdownAuthKey  BreakdownDimension = "auth_key"
	BreakdownPeriod   BreakdownDimension = "period"
)

// BreakdownMetric picks the figure that ranks keys for the top-N cut.
type BreakdownMetric string

// Metrics accepted by GetUsageBreakdown.
const (
	BreakdownMetricCost     BreakdownMetric = "cost"
	BreakdownMetricTokens   BreakdownMetric = "tokens"
	BreakdownMetricRequests BreakdownMetric = "requests"
)

const (
	// DefaultBreakdownLimit is how many row and column keys a breakdown
	// keeps before folding the rest into the "other" bucket.
	DefaultBreakdownLimit = 10
	// MaxBreakdownLimit caps the requested limit.
	MaxBreakdownLimit = 50
	// MaxBreakdownUserPathDepth caps user_path_depth.
	MaxBreakdownUserPathDepth = 16
	// maxBreakdownGroups bounds the (row, column) groups a storage query may
	// return before the top-N fold, so one request cannot pull an unbounded
	// result set.
	maxBreakdownGroups = 50000
)

var breakdownDimensions = map[BreakdownDimension]bool{
	BreakdownModel: true, BreakdownProvider: true, BreakdownUserPath: true, BreakdownLabel: true,
	BreakdownEndpoint: true, BreakdownAuthKey: true, BreakdownPeriod: true,
}

// UsageBreakdownParams configures a two-dimension usage breakdown.
type UsageBreakdownParams struct {
	UsageQueryParams
	Rows    BreakdownDimension
	Columns BreakdownDimension // optional: BreakdownNone yields one column
	Metric  BreakdownMetric
	Limit   int
	// UserPathDepth groups user paths by their first N segments, so depth 2
	// folds /team/alice/laptop into /team/alice. Zero keeps full paths.
	UserPathDepth int
}

// UsageBreakdown is a bounded pivot table. Cells reference Rows and Columns
// by index; keys beyond the limit fold into a trailing "other" key. Period
// keys are kept in chronological order and never folded.
type UsageBreakdown struct {
	RowDimension    BreakdownDimension `json:"row_dimension"`
	ColumnDimension BreakdownDimension `json:"column_dimension"`
	Metric          BreakdownMetric    `json:"metric"`
	Rows            []BreakdownKey     `json:"rows"`
	Columns         []BreakdownKey     `json:"columns"`
	Cells           []BreakdownCell    `json:"cells"`
}

// BreakdownKey is one row or column of a breakdown. An empty Key groups rows
// without a value (no label, no API key). Other marks the bucket holding the
// Folded keys beyond the limit.
type BreakdownKey struct {
	Key    string `json:"key"`
	Other  bool   `json:"other,omitempty"`
	Folded int    `json:"folded,omitempty"`
}

// BreakdownCell holds the totals of one (row, column) pair. A request with
// several labels counts once per label, so label cells overlap.
type BreakdownCell struct {
	Row          int      `json:"row"`
	Column       int      `json:"column"`
	Requests     int      `json:"requests"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	TotalCost    *float64 `json:"total_cost" extensions:"x-nullable"`
	BilledCost   *float64 `json:"billed_cost" extensions:"x-nullable"`
}

// breakdownGroup is one (row, column) aggregate as storage returns it.
type breakdownGroup struct {
	Row, Column  string
	Requests     int
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	TotalCost    *float64
	BilledCost   *float64
}

func (g *breakdownGroup) add(other breakdownGroup) {
	g.Requests += other.Requests
	g.InputTokens += other.InputTokens
	g.OutputTokens += other.OutputTokens
	g.TotalTokens += other.TotalTokens
	g.TotalCost = addCost(g.TotalCost, other.TotalCost)
	g.BilledCost = addCost(g.BilledCost, other.BilledCost)
}

func (g breakdownGroup) metric(metric BreakdownMetric) float64 {
	switch metric {
	case BreakdownMetricTokens:
		return float64(g.TotalTokens)
	case BreakdownMetricRequests:
		return float64(g.Requests)
	default:
		if g.TotalCost == nil {
			return 0
		}
		return *g.TotalCost
	}
}

// NormalizeUsageBreakdownParams validates the breakdown configuration and
// fills its defaults. Errors are invalid-request gateway errors.
func NormalizeUsageBreakdownParams(params UsageBreakdownParams) (UsageBreakdownParams, error) {
	params.Rows = BreakdownDimension(strings.TrimSpace(string(params.Rows)))
	params.Columns = BreakdownDimension(strings.TrimSpace(string(params.Columns)))
	if params.Columns == "none" {
		params.Columns = BreakdownNone
	}
	if !breakdownDimensions[params.Rows] {
		return params, core.NewInvalidRequestError("invalid rows: use one of model, provider, user_path, label, endpoint, auth_key, period", nil)
	}
	if params.Columns != BreakdownNone && !breakdownDimensions[params.Columns] {
		return params, core.NewInvalidRequestError("invalid columns: use one of model, provider, user_path, label, endpoint, auth_key, period", nil)
	}
	if params.Columns == params.Rows {
		return params, core.NewInvalidRequestError("rows and columns must be different dimensions", nil)
	}
	switch params.Metric {
	case "":
		params.Metric = BreakdownMetricCost
	case BreakdownMetricCost, BreakdownMetricTokens, BreakdownMetricRequests:
	default:
		return params, core.NewInvalidRequestError("invalid metric: use one of cost, tokens, requests", nil)
	}
	if params.Limit <= 0 {
		params.Limit = DefaultBreakdownLimit
	}
	params.Limit = min(params.Limit, MaxBreakdownLimit)
	if params.UserPathDepth < 0 || params.UserPathDepth > MaxBreakdownUserPathDepth {
		return params, core.NewInvalidRequestError("invalid user_path_depth: use 0 to 16", nil)
	}
	return params, nil
}

// usesDimension reports whether rows or columns break down by dim.
func (p UsageBreakdownParams) usesDimension(dim BreakdownDimension) bool {
	return p.Rows == dim || p.Columns == dim
}

func tooManyBreakdownGroupsError() error {
	return core.NewInvalidRequestError("usage breakdown has too many groups; narrow the filters or the date range", nil)
}

// buildUsageBreakdown folds storage groups into the bounded pivot: it groups
// user paths by depth, keeps the top keys of each dimension by the chosen
// metric, and folds the rest into a trailing "other" key.
func buildUsageBreakdown(groups []breakdownGroup, params UsageBreakdownParams) *UsageBreakdown {
	merged := map[[2]string]*breakdownGroup{}
	rowTotals := map[string]*breakdownGroup{}
	columnTotals := map[string]*breakdownGroup{}
	for _, group := range groups {
		group.Row = breakdownKey(params.Rows, group.Row, params.UserPathDepth)
		group.Column = breakdownKey(params.Columns, group.Column, params.UserPathDepth)
		accumulateBreakdown(merged, [2]string{group.Row, group.Column}, group)
		accumulateBreakdown(rowTotals, group.Row, group)
		accumulateBreakdown(columnTotals, group.Column, group)
	}

	rows, rowIndex := selectBreakdownKeys(rowTotals, params.Rows, params.Metric, params.Limit)
	columns, columnIndex := selectBreakdownKeys(columnTotals, params.Columns, params.Metric, params.Limit)

	cells := map[[2]int]*breakdownGroup{}
	for key, group := range merged {
		accumulateBreakdown(cells, [2]int{rowIndex(key[0]), columnIndex(key[1])}, *group)
	}
	result := &UsageBreakdown{
		RowDimension:    params.Rows,
		ColumnDimension: params.Columns,
		Metric:          params.Metric,
		Rows:            rows,
		Columns:         columns,
		Cells:           make([]BreakdownCell, 0, len(cells)),
	}
	for index, group := range cells {
		result.Cells = append(result.Cells, BreakdownCell{
			Row: index[0], Column: index[1],
			Requests: group.Requests, InputTokens: group.InputTokens, OutputTokens: group.OutputTokens,
			TotalTokens: group.TotalTokens, TotalCost: group.TotalCost, BilledCost: group.BilledCost,
		})
	}
	slices.SortFunc(result.Cells, func(a, b BreakdownCell) int {
		return cmp.Or(cmp.Compare(a.Row, b.Row), cmp.Compare(a.Column, b.Column))
	})
	return result
}

// accumulateBreakdown adds group to the aggregate stored under key.
func accumulateBreakdown[K comparable](into map[K]*breakdownGroup, key K, group breakdownGroup) {
	if existing, ok := into[key]; ok {
		existing.add(group)
		return
	}
	copied := group
	into[key] = &copied
}

// selectBreakdownKeys orders a dimension's keys and returns them with a
// lookup from key to index. Period keys stay chronological and complete;
// other keys rank by metric (ties by key) and those past limit share a
// trailing "other" key.
func selectBreakdownKeys(totals map[string]*breakdownGroup, dim BreakdownDimension, metric BreakdownMetric, limit int) ([]BreakdownKey, func(string) int) {
	keys := make([]string, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	if dim == BreakdownPeriod || dim == BreakdownNone {
		slices.Sort(keys)
	} else {
		slices.SortFunc(keys, func(a, b string) int {
			return cmp.Or(cmp.Compare(totals[b].metric(metric), totals[a].metric(metric)), cmp.Compare(a, b))
		})
	}

	kept := keys
	folded := 0
	if dim != BreakdownPeriod && dim != BreakdownNone && len(keys) > limit {
		kept, folded = keys[:limit], len(keys)-limit
	}
	result := make([]BreakdownKey, 0, len(kept)+1)
	index := make(map[string]int, len(kept))
	for i, key := range kept {
		result = append(result, BreakdownKey{Key: key})
		index[key] = i
	}
	otherIndex := len(kept)
	if folded > 0 {
		result = append(result, BreakdownKey{Other: true, Folded: folded})
	}
	return result, func(key string) int {
		if i, ok := index[key]; ok {
			return i
		}
		return otherIndex
	}
}

// breakdownKey normalizes a storage key: user paths are cut to depth.
func breakdownKey(dim BreakdownDimension, key string, depth int) string {
	if dim == BreakdownUserPath {
		return truncateUserPath(key, depth)
	}
	return key
}

// truncateUserPath keeps the first depth segments of a canonical user path;
// depth 0 keeps the whole path.
func truncateUserPath(path string, depth int) string {
	if path == "" {
		return "/"
	}
	if depth <= 0 || path == "/" {
		return path
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) <= depth {
		return path
	}
	return "/" + strings.Join(segments[:depth], "/")
}
