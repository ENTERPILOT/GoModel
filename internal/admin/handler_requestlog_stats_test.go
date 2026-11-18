package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/stretchr/testify/require"
)

func TestRequestLogStats_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/request-logs/stats?days=7")
	require.NoError(t, h.RequestLogStats(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[requestlog.RequestStats](t, rec)
	require.Equal(t, requestlog.StatsIntervalDay, result.Interval)
	require.NotNil(t, result.Buckets)
	require.Empty(t, result.Buckets)
	require.NotNil(t, result.ProviderLatency)
	require.Empty(t, result.ProviderLatency)
}

func TestRequestLogStats_InvalidDate(t *testing.T) {
	h := NewHandler(nil, nil, WithRequestLogReader(&mockRequestLogReader{}))
	c, rec := echotest.Get(t, "/admin/request-logs/stats?start_date=not-a-date")
	require.NoError(t, h.RequestLogStats(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid start_date")
}

func TestRequestLogStats_NilReaderInvalidDate(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/request-logs/stats?start_date=not-a-date")
	require.NoError(t, h.RequestLogStats(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRequestLogStats_IntervalFollowsRangeSpan(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{query: "days=1", want: requestlog.StatsIntervalHour},
		{query: "days=3", want: requestlog.StatsIntervalHour},
		{query: "days=4", want: requestlog.StatsIntervalDay},
		{query: "days=30", want: requestlog.StatsIntervalDay},
	}

	for _, tc := range cases {
		reader := &mockRequestLogReader{statsResult: requestlog.EmptyRequestStats("")}
		h := NewHandler(nil, nil, WithRequestLogReader(reader))
		c, rec := echotest.Get(t, "/admin/request-logs/stats?"+tc.query)
		require.NoError(t, h.RequestLogStats(c))
		require.Equal(t, http.StatusOK, rec.Code, tc.query)
		require.Equal(t, tc.want, reader.lastStatsParams.Interval, tc.query)
		require.NotNil(t, reader.lastStatsParams.Location, tc.query)
		require.False(t, reader.lastStatsParams.Now.IsZero(), tc.query)
	}
}

func TestRequestLogStats_PassesThroughReaderResult(t *testing.T) {
	start := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	rate := 0.5
	reader := &mockRequestLogReader{
		statsResult: &requestlog.RequestStats{
			Interval: requestlog.StatsIntervalDay,
			Buckets: []requestlog.RequestStatsBucket{
				{Start: start, Requests: 4, Status2xx: 2, Status4xx: 1, Status5xx: 1},
			},
			Summary: requestlog.RequestStatsSummary{Requests: 4, Status2xx: 2, SuccessRate: &rate},
			ProviderLatency: []requestlog.ProviderLatencySeries{
				{Provider: "openai", Requests: []int64{2}, AvgDurationMs: []*float64{&rate}},
			},
		},
	}
	h := NewHandler(nil, nil, WithRequestLogReader(reader))
	c, rec := echotest.Get(t, "/admin/request-logs/stats?days=7")
	require.NoError(t, h.RequestLogStats(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[requestlog.RequestStats](t, rec)
	require.Len(t, result.Buckets, 1)
	require.Equal(t, int64(4), result.Buckets[0].Requests)
	require.NotNil(t, result.Summary.SuccessRate)
	require.Equal(t, 0.5, *result.Summary.SuccessRate)
	require.Len(t, result.ProviderLatency, 1)
	require.Equal(t, "openai", result.ProviderLatency[0].Provider)
}
