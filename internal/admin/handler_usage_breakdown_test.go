package admin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestUsageBreakdown_Params(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
		check      func(t *testing.T, params usage.UsageBreakdownParams)
	}{
		{
			name:       "defaults",
			query:      "rows=user_path",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, params usage.UsageBreakdownParams) {
				assert.Equal(t, usage.BreakdownUserPath, params.Rows)
				assert.Equal(t, usage.BreakdownNone, params.Columns)
				assert.Equal(t, usage.BreakdownMetricCost, params.Metric)
				assert.Equal(t, usage.DefaultBreakdownLimit, params.Limit)
				assert.Equal(t, "daily", params.Interval)
			},
		},
		{
			name:       "full configuration with filters",
			query:      "rows=user_path&columns=period&interval=monthly&metric=tokens&limit=500&user_path_depth=2&model=a&model=b",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, params usage.UsageBreakdownParams) {
				assert.Equal(t, usage.BreakdownPeriod, params.Columns)
				assert.Equal(t, "monthly", params.Interval)
				assert.Equal(t, usage.BreakdownMetricTokens, params.Metric)
				assert.Equal(t, usage.MaxBreakdownLimit, params.Limit)
				assert.Equal(t, 2, params.UserPathDepth)
				assert.Equal(t, []string{"a", "b"}, params.Models)
			},
		},
		{name: "missing rows", query: "", wantStatus: http.StatusBadRequest},
		{name: "unknown rows", query: "rows=session", wantStatus: http.StatusBadRequest},
		{name: "unknown columns", query: "rows=model&columns=bogus", wantStatus: http.StatusBadRequest},
		{name: "same dimension twice", query: "rows=model&columns=model", wantStatus: http.StatusBadRequest},
		{name: "unknown metric", query: "rows=model&metric=latency", wantStatus: http.StatusBadRequest},
		{name: "non-integer limit", query: "rows=model&limit=ten", wantStatus: http.StatusBadRequest},
		{name: "depth out of range", query: "rows=user_path&user_path_depth=99", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &mockUsageReader{breakdown: &usage.UsageBreakdown{Rows: []usage.BreakdownKey{{Key: "x"}}}}
			h := NewHandler(reader, nil)
			c, rec := echotest.Get(t, "/admin/usage/breakdown?"+tt.query)
			require.NoError(t, h.UsageBreakdown(c))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.check != nil {
				tt.check(t, reader.lastBreakdown)
			}
		})
	}
}

func TestUsageBreakdown_DisabledReaderReturnsEmptyTable(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/breakdown?rows=model&columns=period")
	require.NoError(t, h.UsageBreakdown(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"row_dimension":"model","column_dimension":"period","metric":"cost","rows":[],"columns":[],"cells":[]}`, rec.Body.String())
}
