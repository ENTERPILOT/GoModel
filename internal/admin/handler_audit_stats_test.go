package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/require"
)

func TestAuditStats_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/audit/stats?days=7")
	require.NoError(t, h.AuditStats(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.RequestStats](t, rec)
	require.Equal(t, auditlog.StatsIntervalDay, result.Interval)
	require.NotNil(t, result.Buckets)
	require.Empty(t, result.Buckets)
	require.NotNil(t, result.ProviderLatency)
	require.Empty(t, result.ProviderLatency)
}

func TestAuditStats_InvalidDate(t *testing.T) {
	h := NewHandler(nil, nil, WithAuditReader(&mockAuditReader{}))
	c, rec := echotest.Get(t, "/admin/audit/stats?start_date=not-a-date")
	require.NoError(t, h.AuditStats(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid start_date")
}

func TestAuditStats_NilReaderInvalidDate(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/audit/stats?start_date=not-a-date")
	require.NoError(t, h.AuditStats(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAuditStats_IntervalFollowsRangeSpan(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{query: "days=1", want: auditlog.StatsIntervalHour},
		{query: "days=3", want: auditlog.StatsIntervalHour},
		{query: "days=4", want: auditlog.StatsIntervalDay},
		{query: "days=30", want: auditlog.StatsIntervalDay},
	}

	for _, tc := range cases {
		reader := &mockAuditReader{statsResult: auditlog.EmptyRequestStats("")}
		h := NewHandler(nil, nil, WithAuditReader(reader))
		c, rec := echotest.Get(t, "/admin/audit/stats?"+tc.query)
		require.NoError(t, h.AuditStats(c))
		require.Equal(t, http.StatusOK, rec.Code, tc.query)
		require.Equal(t, tc.want, reader.lastStatsParams.Interval, tc.query)
		require.NotNil(t, reader.lastStatsParams.Location, tc.query)
		require.False(t, reader.lastStatsParams.Now.IsZero(), tc.query)
	}
}

func TestAuditStats_PassesThroughReaderResult(t *testing.T) {
	start := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	rate := 0.5
	reader := &mockAuditReader{
		statsResult: &auditlog.RequestStats{
			Interval: auditlog.StatsIntervalDay,
			Buckets: []auditlog.RequestStatsBucket{
				{Start: start, Requests: 4, Status2xx: 2, Status4xx: 1, Status5xx: 1},
			},
			Summary: auditlog.RequestStatsSummary{Requests: 4, Status2xx: 2, SuccessRate: &rate},
			ProviderLatency: []auditlog.ProviderLatencySeries{
				{Provider: "openai", Requests: []int64{2}, AvgDurationMs: []*float64{&rate}},
			},
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/stats?days=7")
	require.NoError(t, h.AuditStats(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.RequestStats](t, rec)
	require.Len(t, result.Buckets, 1)
	require.Equal(t, int64(4), result.Buckets[0].Requests)
	require.NotNil(t, result.Summary.SuccessRate)
	require.Equal(t, 0.5, *result.Summary.SuccessRate)
	require.Len(t, result.ProviderLatency, 1)
	require.Equal(t, "openai", result.ProviderLatency[0].Provider)
}
