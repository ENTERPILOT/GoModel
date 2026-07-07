package auditlog

import (
	"sort"
	"time"
)

// Request stats bucket granularities. The admin handler picks hourly buckets
// for short ranges and daily buckets for longer ones.
const (
	StatsIntervalHour = "hour"
	StatsIntervalDay  = "day"
)

// statsHourLayout parses the UTC hour keys the storage backends group by.
const statsHourLayout = "2006-01-02T15"

// RequestStatsParams selects the range and bucketing for GetRequestStats.
type RequestStatsParams struct {
	QueryParams

	// Interval is the bucket granularity: StatsIntervalHour or StatsIntervalDay.
	Interval string

	// Location is the dashboard timezone. Daily buckets start at local
	// midnight in this location; hourly buckets are timezone-independent.
	Location *time.Location

	// Now bounds zero-filling: buckets are emitted from the range start up to
	// the earlier of the range end and Now, so a range ending today does not
	// trail empty future buckets.
	Now time.Time
}

// RequestStats is the time-bucketed request breakdown for the dashboard's
// status-code and provider-latency charts.
type RequestStats struct {
	Interval        string                  `json:"interval"`
	Buckets         []RequestStatsBucket    `json:"buckets"`
	Summary         RequestStatsSummary     `json:"summary"`
	ProviderLatency []ProviderLatencySeries `json:"provider_latency"`
}

// RequestStatsBucket counts requests by status class within one time bucket.
type RequestStatsBucket struct {
	Start       time.Time `json:"start"`
	Requests    int64     `json:"requests"`
	Status2xx   int64     `json:"status_2xx"`
	Status4xx   int64     `json:"status_4xx"`
	Status5xx   int64     `json:"status_5xx"`
	StatusOther int64     `json:"status_other"`
}

// RequestStatsSummary aggregates the whole range. SuccessRate is the 2xx share
// of all requests; AvgDurationMs averages successful, uncached requests. Both
// are nil when the range has no qualifying requests.
type RequestStatsSummary struct {
	Requests      int64    `json:"requests"`
	Status2xx     int64    `json:"status_2xx"`
	Status4xx     int64    `json:"status_4xx"`
	Status5xx     int64    `json:"status_5xx"`
	StatusOther   int64    `json:"status_other"`
	SuccessRate   *float64 `json:"success_rate,omitempty"`
	AvgDurationMs *float64 `json:"avg_duration_ms,omitempty"`
}

// ProviderLatencySeries is one provider's average request duration per bucket,
// aligned index-by-index with RequestStats.Buckets. Entries are nil for
// buckets where the provider served no successful uncached request, so charts
// can render gaps instead of misleading zeros. Durations are gateway-observed
// request durations of successful (2xx) requests, excluding local cache hits.
type ProviderLatencySeries struct {
	Provider      string     `json:"provider"`
	Requests      []int64    `json:"requests"`
	AvgDurationMs []*float64 `json:"avg_duration_ms"`
}

// statsRow is one (UTC hour, provider) aggregate scanned from a storage
// backend. Duration sums cover only latency-eligible requests: status 2xx,
// duration recorded, and not served from the local response cache.
type statsRow struct {
	HourUTC       time.Time
	Provider      string
	Requests      int64
	Status2xx     int64
	Status4xx     int64
	Status5xx     int64
	DurationNsSum int64
	DurationCount int64
}

// EmptyRequestStats returns a zero-value result for the disabled-reader fast
// path so the response shape matches an enabled reader's.
func EmptyRequestStats(interval string) *RequestStats {
	return &RequestStats{
		Interval:        normalizeStatsInterval(interval),
		Buckets:         []RequestStatsBucket{},
		ProviderLatency: []ProviderLatencySeries{},
	}
}

func normalizeStatsInterval(interval string) string {
	if interval == StatsIntervalHour {
		return StatsIntervalHour
	}
	return StatsIntervalDay
}

// foldRequestStats aggregates per-hour rows into the requested bucket
// granularity. Backends group by UTC hour so one query serves both
// granularities; folding hours into local days here keeps daily bucketing
// correct across DST transitions without per-backend timezone SQL.
func foldRequestStats(rows []statsRow, params RequestStatsParams) *RequestStats {
	interval := normalizeStatsInterval(params.Interval)
	location := params.Location
	if location == nil {
		location = time.UTC
	}
	now := params.Now
	if now.IsZero() {
		now = time.Now()
	}

	bucketStart := func(t time.Time) time.Time {
		if interval == StatsIntervalHour {
			return t.UTC().Truncate(time.Hour)
		}
		local := t.In(location)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	}
	nextBucket := func(t time.Time) time.Time {
		if interval == StatsIntervalHour {
			return t.Add(time.Hour)
		}
		return t.AddDate(0, 0, 1)
	}

	buckets := make(map[int64]*RequestStatsBucket)
	ensureBucket := func(start time.Time) *RequestStatsBucket {
		key := start.Unix()
		if b, ok := buckets[key]; ok {
			return b
		}
		b := &RequestStatsBucket{Start: start}
		buckets[key] = b
		return b
	}

	// Zero-fill the requested range so quiet periods stay visible, but never
	// past "now": a range ending today should stop at the current bucket.
	if !params.StartDate.IsZero() && !params.EndDate.IsZero() {
		endExclusive := params.EndDate.In(location).AddDate(0, 0, 1)
		for start := bucketStart(params.StartDate); start.Before(endExclusive) && !start.After(now); start = nextBucket(start) {
			ensureBucket(start)
		}
	}

	type latencyCell struct {
		durationNs int64
		requests   int64
	}
	providerCells := make(map[string]map[int64]*latencyCell)
	providerRequests := make(map[string]int64)

	var summary RequestStatsSummary
	var totalDurationNs, totalDurationCount int64
	for _, row := range rows {
		start := bucketStart(row.HourUTC)
		b := ensureBucket(start)
		b.Requests += row.Requests
		b.Status2xx += row.Status2xx
		b.Status4xx += row.Status4xx
		b.Status5xx += row.Status5xx

		summary.Requests += row.Requests
		summary.Status2xx += row.Status2xx
		summary.Status4xx += row.Status4xx
		summary.Status5xx += row.Status5xx
		totalDurationNs += row.DurationNsSum
		totalDurationCount += row.DurationCount

		if row.Provider == "" || row.DurationCount == 0 {
			continue
		}
		cells, ok := providerCells[row.Provider]
		if !ok {
			cells = make(map[int64]*latencyCell)
			providerCells[row.Provider] = cells
		}
		cell, ok := cells[start.Unix()]
		if !ok {
			cell = &latencyCell{}
			cells[start.Unix()] = cell
		}
		cell.durationNs += row.DurationNsSum
		cell.requests += row.DurationCount
		providerRequests[row.Provider] += row.DurationCount
	}

	ordered := make([]RequestStatsBucket, 0, len(buckets))
	for _, b := range buckets {
		b.StatusOther = b.Requests - b.Status2xx - b.Status4xx - b.Status5xx
		ordered = append(ordered, *b)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })

	summary.StatusOther = summary.Requests - summary.Status2xx - summary.Status4xx - summary.Status5xx
	if summary.Requests > 0 {
		rate := float64(summary.Status2xx) / float64(summary.Requests)
		summary.SuccessRate = &rate
	}
	if totalDurationCount > 0 {
		avg := float64(totalDurationNs) / float64(totalDurationCount) / 1e6
		summary.AvgDurationMs = &avg
	}

	providers := make([]string, 0, len(providerCells))
	for provider := range providerCells {
		providers = append(providers, provider)
	}
	// Busiest providers first so chart legend order matches relevance.
	sort.Slice(providers, func(i, j int) bool {
		if providerRequests[providers[i]] != providerRequests[providers[j]] {
			return providerRequests[providers[i]] > providerRequests[providers[j]]
		}
		return providers[i] < providers[j]
	})

	latency := make([]ProviderLatencySeries, 0, len(providers))
	for _, provider := range providers {
		cells := providerCells[provider]
		series := ProviderLatencySeries{
			Provider:      provider,
			Requests:      make([]int64, len(ordered)),
			AvgDurationMs: make([]*float64, len(ordered)),
		}
		for i, b := range ordered {
			if cell, ok := cells[b.Start.Unix()]; ok && cell.requests > 0 {
				avg := float64(cell.durationNs) / float64(cell.requests) / 1e6
				series.Requests[i] = cell.requests
				series.AvgDurationMs[i] = &avg
			}
		}
		latency = append(latency, series)
	}

	return &RequestStats{
		Interval:        interval,
		Buckets:         ordered,
		Summary:         summary,
		ProviderLatency: latency,
	}
}
