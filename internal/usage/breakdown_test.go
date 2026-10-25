package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateUserPath(t *testing.T) {
	tests := []struct {
		path  string
		depth int
		want  string
	}{
		{"/team/alice/laptop", 0, "/team/alice/laptop"},
		{"/team/alice/laptop", 1, "/team"},
		{"/team/alice/laptop", 2, "/team/alice"},
		{"/team/alice/laptop", 3, "/team/alice/laptop"},
		{"/team/alice/laptop", 9, "/team/alice/laptop"},
		{"/", 2, "/"},
		{"", 2, "/"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, truncateUserPath(tt.path, tt.depth), "%s depth %d", tt.path, tt.depth)
	}
}

func TestNormalizeUsageBreakdownParams(t *testing.T) {
	tests := []struct {
		name    string
		params  UsageBreakdownParams
		want    UsageBreakdownParams
		wantErr string
	}{
		{
			name:   "defaults",
			params: UsageBreakdownParams{Rows: " model "},
			want:   UsageBreakdownParams{Rows: BreakdownModel, Metric: BreakdownMetricCost, Limit: DefaultBreakdownLimit},
		},
		{
			name:   "limit capped",
			params: UsageBreakdownParams{Rows: BreakdownLabel, Columns: BreakdownPeriod, Metric: BreakdownMetricRequests, Limit: 1000},
			want:   UsageBreakdownParams{Rows: BreakdownLabel, Columns: BreakdownPeriod, Metric: BreakdownMetricRequests, Limit: MaxBreakdownLimit},
		},
		{
			name:   "columns none means one column",
			params: UsageBreakdownParams{Rows: BreakdownModel, Columns: "none"},
			want:   UsageBreakdownParams{Rows: BreakdownModel, Metric: BreakdownMetricCost, Limit: DefaultBreakdownLimit},
		},
		{name: "rows required", params: UsageBreakdownParams{}, wantErr: "invalid rows"},
		{name: "unknown columns", params: UsageBreakdownParams{Rows: BreakdownModel, Columns: "session"}, wantErr: "invalid columns"},
		{name: "same dimension", params: UsageBreakdownParams{Rows: BreakdownModel, Columns: BreakdownModel}, wantErr: "must be different"},
		{name: "unknown metric", params: UsageBreakdownParams{Rows: BreakdownModel, Metric: "latency"}, wantErr: "invalid metric"},
		{name: "negative depth", params: UsageBreakdownParams{Rows: BreakdownUserPath, UserPathDepth: -1}, wantErr: "user_path_depth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeUsageBreakdownParams(tt.params)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildUsageBreakdown(t *testing.T) {
	group := func(row, column string, requests int, tokens int64, cost *float64) breakdownGroup {
		return breakdownGroup{Row: row, Column: column, Requests: requests, TotalTokens: tokens, InputTokens: tokens, TotalCost: cost, BilledCost: cost}
	}
	tests := []struct {
		name        string
		groups      []breakdownGroup
		params      UsageBreakdownParams
		wantRows    []BreakdownKey
		wantColumns []BreakdownKey
		wantCells   []BreakdownCell
	}{
		{
			name: "top keys by cost with other bucket",
			groups: []breakdownGroup{
				group("a", "", 1, 10, new(1.0)),
				group("b", "", 1, 10, new(3.0)),
				group("c", "", 1, 10, new(2.0)),
				group("d", "", 5, 50, nil),
			},
			params:      UsageBreakdownParams{Rows: BreakdownModel, Metric: BreakdownMetricCost, Limit: 2},
			wantRows:    []BreakdownKey{{Key: "b"}, {Key: "c"}, {Other: true, Folded: 2}},
			wantColumns: []BreakdownKey{{Key: ""}},
			wantCells: []BreakdownCell{
				{Row: 0, Column: 0, Requests: 1, InputTokens: 10, TotalTokens: 10, TotalCost: new(3.0), BilledCost: new(3.0)},
				{Row: 1, Column: 0, Requests: 1, InputTokens: 10, TotalTokens: 10, TotalCost: new(2.0), BilledCost: new(2.0)},
				{Row: 2, Column: 0, Requests: 6, InputTokens: 60, TotalTokens: 60, TotalCost: new(1.0), BilledCost: new(1.0)},
			},
		},
		{
			name: "requests metric ranks and ties break by key",
			groups: []breakdownGroup{
				group("b", "", 2, 1, nil),
				group("a", "", 2, 1, nil),
				group("c", "", 5, 1, nil),
			},
			params:      UsageBreakdownParams{Rows: BreakdownLabel, Metric: BreakdownMetricRequests, Limit: 10},
			wantRows:    []BreakdownKey{{Key: "c"}, {Key: "a"}, {Key: "b"}},
			wantColumns: []BreakdownKey{{Key: ""}},
			wantCells: []BreakdownCell{
				{Row: 0, Column: 0, Requests: 5, InputTokens: 1, TotalTokens: 1},
				{Row: 1, Column: 0, Requests: 2, InputTokens: 1, TotalTokens: 1},
				{Row: 2, Column: 0, Requests: 2, InputTokens: 1, TotalTokens: 1},
			},
		},
		{
			name: "user paths grouped by depth and periods kept chronological",
			groups: []breakdownGroup{
				group("/team/alice/laptop", "2026-02", 1, 100, nil),
				group("/team/alice/ci", "2026-01", 1, 50, nil),
				group("/team/bob", "2026-01", 1, 10, nil),
				group("/", "2026-03", 1, 1, nil),
			},
			params:      UsageBreakdownParams{Rows: BreakdownUserPath, Columns: BreakdownPeriod, Metric: BreakdownMetricTokens, Limit: 1, UserPathDepth: 2},
			wantRows:    []BreakdownKey{{Key: "/team/alice"}, {Other: true, Folded: 2}},
			wantColumns: []BreakdownKey{{Key: "2026-01"}, {Key: "2026-02"}, {Key: "2026-03"}},
			wantCells: []BreakdownCell{
				{Row: 0, Column: 0, Requests: 1, InputTokens: 50, TotalTokens: 50},
				{Row: 0, Column: 1, Requests: 1, InputTokens: 100, TotalTokens: 100},
				{Row: 1, Column: 0, Requests: 1, InputTokens: 10, TotalTokens: 10},
				{Row: 1, Column: 2, Requests: 1, InputTokens: 1, TotalTokens: 1},
			},
		},
		{
			name:        "empty input",
			params:      UsageBreakdownParams{Rows: BreakdownModel, Columns: BreakdownProvider, Metric: BreakdownMetricCost, Limit: 5},
			wantRows:    []BreakdownKey{},
			wantColumns: []BreakdownKey{},
			wantCells:   []BreakdownCell{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildUsageBreakdown(tt.groups, tt.params)
			assert.Equal(t, tt.params.Rows, got.RowDimension)
			assert.Equal(t, tt.params.Columns, got.ColumnDimension)
			assert.Equal(t, tt.wantRows, got.Rows)
			assert.Equal(t, tt.wantColumns, got.Columns)
			assert.Equal(t, tt.wantCells, got.Cells)
		})
	}
}
