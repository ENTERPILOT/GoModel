package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

func setTimeNow(t *testing.T, now time.Time) {
	t.Helper()
	original := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = original })
}

func TestUsageBenchmark_Params(t *testing.T) {
	// 2026-11-30 23:30 UTC is already December in Warsaw.
	setTimeNow(t, time.Date(2026, 11, 30, 23, 30, 0, 0, time.UTC))
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	tests := []struct {
		name       string
		query      string
		timeZone   string
		markups    bool
		wantStatus int
		wantCode   string
		wantStart  time.Time
		wantEnd    time.Time
		wantBasis  string
		wantTop    int
	}{
		{
			name: "defaults: six complete months and the current one", wantStatus: http.StatusOK,
			wantStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			wantBasis: usage.BenchmarkCostBasisProvider,
		},
		{
			name: "months follow the request time zone", timeZone: "Europe/Warsaw", wantStatus: http.StatusOK,
			wantStart: time.Date(2026, 6, 1, 0, 0, 0, 0, warsaw), wantEnd: time.Date(2026, 12, 31, 0, 0, 0, 0, warsaw),
			wantBasis: usage.BenchmarkCostBasisProvider,
		},
		{
			name: "explicit range and top users", query: "start_month=2026-01&end_month=2026-02&top_users=5", wantStatus: http.StatusOK,
			wantStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
			wantBasis: usage.BenchmarkCostBasisProvider, wantTop: 5,
		},
		{
			name: "billed with markups", query: "cost_basis=billed", markups: true, wantStatus: http.StatusOK,
			wantStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			wantBasis: usage.BenchmarkCostBasisBilled,
		},
		{
			name: "months counts back from the end month", query: "months=3&end_month=2026-02", wantStatus: http.StatusOK,
			wantStart: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), wantEnd: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
			wantBasis: usage.BenchmarkCostBasisProvider,
		},
		{name: "too many months requested", query: "months=25", wantStatus: http.StatusBadRequest},
		{name: "billed without markups", query: "cost_basis=billed", wantStatus: http.StatusConflict, wantCode: "markups_disabled"},
		{name: "unknown cost basis", query: "cost_basis=list", wantStatus: http.StatusBadRequest},
		{name: "future end month", query: "end_month=2026-12", wantStatus: http.StatusBadRequest},
		{name: "start after end", query: "start_month=2026-10&end_month=2026-09", wantStatus: http.StatusBadRequest},
		{name: "too many months", query: "start_month=2024-11", wantStatus: http.StatusBadRequest},
		{name: "malformed month", query: "start_month=2026-1-01", wantStatus: http.StatusBadRequest},
		{name: "too many top users", query: "top_users=51", wantStatus: http.StatusBadRequest},
		{name: "invalid parent", query: "parent=/a/../b", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &mockUsageReader{}
			h := NewHandler(reader, nil, WithBilling(usage.Billing{Enabled: tt.markups}))
			var opts []echotest.Option
			if tt.timeZone != "" {
				opts = append(opts, echotest.WithHeader(dashboardTimeZoneHeader, tt.timeZone))
			}
			c, rec := echotest.Get(t, "/admin/usage/benchmark?"+tt.query, opts...)
			require.NoError(t, h.UsageBenchmark(c))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantCode != "" {
				assert.Contains(t, rec.Body.String(), `"code":"`+tt.wantCode+`"`)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			got := reader.lastBenchmark
			assert.True(t, tt.wantStart.Equal(got.StartDate), "start %s", got.StartDate)
			assert.True(t, tt.wantEnd.Equal(got.EndDate), "end %s", got.EndDate)
			assert.Equal(t, tt.wantBasis, got.CostBasis)
			assert.Equal(t, tt.wantTop, got.TopUsers)
			// The mock returns nil, so the handler answers with the empty months.
			assert.Contains(t, rec.Body.String(), `"months":[`)
		})
	}
}

func TestUsageBenchmark_EmptyMonthsWithoutReader(t *testing.T) {
	setTimeNow(t, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/benchmark?start_month=2026-02&parent=/eng")
	require.NoError(t, h.UsageBenchmark(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"parent":"/eng","time_zone":"UTC","currency":"USD","cost_basis":"provider","months":[
		{"month":"2026-02","partial":false,"active_users":0,"unpriced_users":0,"total_cost":0,"mean":null,"p50":null,"p90":null,"max":null,"mean_change_percent":null,"p50_change_percent":null,"unattributed_cost":0},
		{"month":"2026-03","partial":true,"active_users":0,"unpriced_users":0,"total_cost":0,"mean":null,"p50":null,"p90":null,"max":null,"mean_change_percent":null,"p50_change_percent":null,"unattributed_cost":0}
	]}`, rec.Body.String())
}

func TestUsageBenchmark_Scope(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		scope      string
		wantStatus int
		wantParent string
		wantScope  string
	}{
		{name: "global without parent benchmarks the root's children", wantStatus: http.StatusOK},
		{name: "scoped without parent defaults to the scope root", scope: scopeAlpha, wantStatus: http.StatusOK, wantParent: scopeAlpha, wantScope: scopeAlpha},
		{name: "parent inside scope", scope: scopeAlpha, query: "parent=" + scopeAlpha + "/apps", wantStatus: http.StatusOK, wantParent: scopeAlpha + "/apps", wantScope: scopeAlpha},
		{name: "parent above scope", scope: scopeAlpha, query: "parent=/team", wantStatus: http.StatusForbidden},
		{name: "root parent", scope: scopeAlpha, query: "parent=/", wantStatus: http.StatusForbidden},
		{name: "user path filter outside scope", scope: scopeAlpha, query: "user_path=/team/beta", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &mockUsageReader{}
			h := NewHandler(reader, nil)
			c, rec := scopedRequest(t, http.MethodGet, "/admin/usage/benchmark?"+tt.query, "", tt.scope)
			require.NoError(t, h.UsageBenchmark(c))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusOK {
				assert.Contains(t, rec.Body.String(), `"code":"user_path_out_of_scope"`)
				return
			}
			assert.Equal(t, tt.wantParent, reader.lastBenchmark.Parent)
			assert.Equal(t, tt.wantScope, reader.lastBenchmark.UserPath)
		})
	}
}
