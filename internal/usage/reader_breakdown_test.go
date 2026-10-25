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

type usageBatchWriter interface {
	WriteBatch(ctx context.Context, entries []*UsageEntry) error
}

// multiFilterFixture: four provider requests across teams, keys, labels and
// two months, plus a local cache hit the default cache mode excludes.
func multiFilterFixture() []*UsageEntry {
	entry := func(id, model, provider, providerName, userPath, authKey string, labels []string, tokens int, cost, billed *float64, ts time.Time) *UsageEntry {
		return &UsageEntry{
			ID: "7a2d0000-0000-4000-8000-00000000000" + id, RequestID: "req-" + id, ProviderID: "p-" + id,
			Timestamp: ts, Model: model, Provider: provider, ProviderName: providerName,
			Endpoint: "/v1/chat/completions", UserPath: userPath, AuthKeyID: authKey, Labels: labels,
			InputTokens: tokens, OutputTokens: 0, TotalTokens: tokens, TotalCost: cost, BilledCost: billed,
		}
	}
	jan := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	hit := entry("5", "gpt-5", "openai", "primary", "/team/alice/laptop", "key-a", []string{"dev"}, 1000, new(9.0), nil, feb)
	hit.CacheType = CacheTypeExact
	return []*UsageEntry{
		entry("1", "gpt-5", "openai", "primary", "/team/alice/laptop", "key-a", []string{"branch:main", "dev"}, 100, new(1.0), new(1.2), jan),
		entry("2", "gpt-5", "openai", "primary", "/team/alice/ci", "key-a", []string{"branch:feat"}, 200, new(2.0), new(2.0), feb),
		entry("3", "claude", "anthropic", "", "/team/bob", "key-b", nil, 300, new(3.0), new(3.0), feb),
		entry("4", "gemini", "gemini", "", "/ops", "key-c", []string{"dev"}, 400, nil, nil, feb),
		hit,
	}
}

var multiFilterRange = UsageQueryParams{
	StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	EndDate:   time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
	TimeZone:  "UTC",
}

func TestReadersMultiValueFiltersAndBreakdown(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertMultiFilterReads(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertMultiFilterReads(t, store, reader)
		})
	})
}

func assertMultiFilterReads(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, multiFilterFixture()))

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			name       string
			apply      func(p *UsageQueryParams)
			wantTokens int64
		}{
			{"no filter", func(p *UsageQueryParams) {}, 1000},
			{"models", func(p *UsageQueryParams) { p.Models = []string{"gpt-5", "claude"} }, 600},
			{"providers match name or type", func(p *UsageQueryParams) { p.Providers = []string{"primary", "anthropic"} }, 600},
			{"labels", func(p *UsageQueryParams) { p.Labels = []string{"branch:main", "branch:feat"} }, 300},
			{"auth keys", func(p *UsageQueryParams) { p.AuthKeyIDs = []string{"key-b", "key-c"} }, 700},
			{"user path subtrees", func(p *UsageQueryParams) { p.UserPaths = []string{"/team/alice", "/ops"} }, 700},
			{"scope narrows user paths", func(p *UsageQueryParams) {
				p.UserPath = "/team"
				p.UserPaths = []string{"/team/alice/ci", "/ops"}
			}, 200},
			{"AND across dimensions", func(p *UsageQueryParams) {
				p.Models = []string{"gpt-5", "gemini"}
				p.Labels = []string{"dev"}
			}, 500},
			{"single value and list both narrow", func(p *UsageQueryParams) {
				p.Model = "gpt-5"
				p.Models = []string{"claude"}
			}, 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				params := multiFilterRange
				tt.apply(&params)
				summary, err := reader.GetSummary(ctx, params)
				require.NoError(t, err)
				assert.Equal(t, tt.wantTokens, summary.TotalTokens)

				log, err := reader.GetUsageLog(ctx, UsageLogParams{UsageQueryParams: params})
				require.NoError(t, err)
				var logTokens int64
				for _, entry := range log.Entries {
					logTokens += int64(entry.TotalTokens)
				}
				assert.Equal(t, tt.wantTokens, logTokens, "request log follows the same filters")
			})
		}
	})

	t.Run("user path breakdown honors several subtrees", func(t *testing.T) {
		params := multiFilterRange
		params.UserPaths = []string{"/team/alice", "/ops"}
		rows, err := reader.GetUsageByUserPath(ctx, params)
		require.NoError(t, err)
		got := map[string]int64{}
		for _, row := range rows {
			got[row.UserPath] = row.TotalTokens
		}
		assert.Equal(t, map[string]int64{"/team/alice/laptop": 100, "/team/alice/ci": 200, "/ops": 400}, got)
	})

	t.Run("breakdown", func(t *testing.T) {
		tests := []struct {
			name        string
			params      UsageBreakdownParams
			wantRows    []BreakdownKey
			wantColumns []BreakdownKey
			wantCells   []BreakdownCell
		}{
			{
				name: "cost per month by user path prefix",
				params: UsageBreakdownParams{
					Rows: BreakdownUserPath, Columns: BreakdownPeriod, UserPathDepth: 2,
					UsageQueryParams: withInterval(multiFilterRange, "monthly"),
				},
				// /team/alice and /team/bob tie on cost (3.0) and sort by key.
				wantRows:    []BreakdownKey{{Key: "/team/alice"}, {Key: "/team/bob"}, {Key: "/ops"}},
				wantColumns: []BreakdownKey{{Key: "2026-01"}, {Key: "2026-02"}},
				wantCells: []BreakdownCell{
					{Row: 0, Column: 0, Requests: 1, InputTokens: 100, TotalTokens: 100, TotalCost: new(1.0), BilledCost: new(1.2)},
					{Row: 0, Column: 1, Requests: 1, InputTokens: 200, TotalTokens: 200, TotalCost: new(2.0), BilledCost: new(2.0)},
					{Row: 1, Column: 1, Requests: 1, InputTokens: 300, TotalTokens: 300, TotalCost: new(3.0), BilledCost: new(3.0)},
					{Row: 2, Column: 1, Requests: 1, InputTokens: 400, TotalTokens: 400},
				},
			},
			{
				name: "labels keep unlabelled requests",
				params: UsageBreakdownParams{
					Rows: BreakdownLabel, Metric: BreakdownMetricTokens, UsageQueryParams: multiFilterRange,
				},
				wantRows:    []BreakdownKey{{Key: "dev"}, {Key: ""}, {Key: "branch:feat"}, {Key: "branch:main"}},
				wantColumns: []BreakdownKey{{Key: ""}},
				wantCells: []BreakdownCell{
					{Row: 0, Column: 0, Requests: 2, InputTokens: 500, TotalTokens: 500, TotalCost: new(1.0), BilledCost: new(1.2)},
					{Row: 1, Column: 0, Requests: 1, InputTokens: 300, TotalTokens: 300, TotalCost: new(3.0), BilledCost: new(3.0)},
					{Row: 2, Column: 0, Requests: 1, InputTokens: 200, TotalTokens: 200, TotalCost: new(2.0), BilledCost: new(2.0)},
					{Row: 3, Column: 0, Requests: 1, InputTokens: 100, TotalTokens: 100, TotalCost: new(1.0), BilledCost: new(1.2)},
				},
			},
			{
				name: "provider by auth key with other bucket and filters",
				params: UsageBreakdownParams{
					Rows: BreakdownProvider, Columns: BreakdownAuthKey, Metric: BreakdownMetricRequests, Limit: 1,
					UsageQueryParams: withUserPaths(multiFilterRange, "/team"),
				},
				wantRows:    []BreakdownKey{{Key: "primary"}, {Other: true, Folded: 1}},
				wantColumns: []BreakdownKey{{Key: "key-a"}, {Other: true, Folded: 1}},
				wantCells: []BreakdownCell{
					{Row: 0, Column: 0, Requests: 2, InputTokens: 300, TotalTokens: 300, TotalCost: new(3.0), BilledCost: new(3.2)},
					{Row: 1, Column: 1, Requests: 1, InputTokens: 300, TotalTokens: 300, TotalCost: new(3.0), BilledCost: new(3.0)},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := reader.GetUsageBreakdown(ctx, tt.params)
				require.NoError(t, err)
				assert.Equal(t, tt.wantRows, got.Rows)
				assert.Equal(t, tt.wantColumns, got.Columns)
				require.Len(t, got.Cells, len(tt.wantCells))
				for i, want := range tt.wantCells {
					cell := got.Cells[i]
					assert.Equal(t, [2]int{want.Row, want.Column}, [2]int{cell.Row, cell.Column}, "cell %d", i)
					assert.Equal(t, want.Requests, cell.Requests, "cell %d", i)
					assert.Equal(t, want.TotalTokens, cell.TotalTokens, "cell %d", i)
					assertCostEqual(t, want.TotalCost, cell.TotalCost, "cell %d total cost", i)
					assertCostEqual(t, want.BilledCost, cell.BilledCost, "cell %d billed cost", i)
				}
			})
		}
	})
}

func withInterval(params UsageQueryParams, interval string) UsageQueryParams {
	params.Interval = interval
	return params
}

func withUserPaths(params UsageQueryParams, paths ...string) UsageQueryParams {
	params.UserPaths = paths
	return params
}

func assertCostEqual(t *testing.T, want, got *float64, msgAndArgs ...any) {
	t.Helper()
	if want == nil {
		assert.Nil(t, got, msgAndArgs...)
		return
	}
	if assert.NotNil(t, got, msgAndArgs...) {
		assert.InDelta(t, *want, *got, 1e-9, msgAndArgs...)
	}
}
