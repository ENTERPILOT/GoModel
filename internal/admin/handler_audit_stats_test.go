package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

func TestAuditStats_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/audit/stats?days=7")

	if err := h.AuditStats(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var result auditlog.RequestStats
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result.Interval != auditlog.StatsIntervalDay {
		t.Fatalf("interval = %q, want day", result.Interval)
	}
	if result.Buckets == nil || len(result.Buckets) != 0 {
		t.Fatalf("buckets = %#v, want empty slice", result.Buckets)
	}
	if result.ProviderLatency == nil || len(result.ProviderLatency) != 0 {
		t.Fatalf("provider_latency = %#v, want empty slice", result.ProviderLatency)
	}
}

func TestAuditStats_InvalidDate(t *testing.T) {
	h := NewHandler(nil, nil, WithAuditReader(&mockAuditReader{}))
	c, rec := newHandlerContext("/admin/audit/stats?start_date=not-a-date")

	if err := h.AuditStats(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid start_date") {
		t.Fatalf("body = %q, want start_date error", rec.Body.String())
	}
}

func TestAuditStats_NilReaderInvalidDate(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/audit/stats?start_date=not-a-date")

	if err := h.AuditStats(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 even with a nil reader", rec.Code)
	}
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
		c, rec := newHandlerContext("/admin/audit/stats?" + tc.query)

		if err := h.AuditStats(c); err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.query, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.query, rec.Code)
		}
		if reader.lastStatsParams.Interval != tc.want {
			t.Fatalf("%s: interval = %q, want %q", tc.query, reader.lastStatsParams.Interval, tc.want)
		}
		if reader.lastStatsParams.Location == nil {
			t.Fatalf("%s: expected a location to be passed", tc.query)
		}
		if reader.lastStatsParams.Now.IsZero() {
			t.Fatalf("%s: expected Now to be passed", tc.query)
		}
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
	c, rec := newHandlerContext("/admin/audit/stats?days=7")

	if err := h.AuditStats(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var result auditlog.RequestStats
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Buckets) != 1 || result.Buckets[0].Requests != 4 {
		t.Fatalf("buckets = %#v", result.Buckets)
	}
	if result.Summary.SuccessRate == nil || *result.Summary.SuccessRate != 0.5 {
		t.Fatalf("success rate = %v, want 0.5", result.Summary.SuccessRate)
	}
	if len(result.ProviderLatency) != 1 || result.ProviderLatency[0].Provider != "openai" {
		t.Fatalf("provider_latency = %#v", result.ProviderLatency)
	}
}
