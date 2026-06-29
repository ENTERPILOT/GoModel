package usage

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// ThroughputGranularity describes one bucket width for the overview live
// token-throughput chart. WindowCount is how many trailing buckets the chart
// shows, so the window spans BucketSize * WindowCount.
type ThroughputGranularity struct {
	Name        string
	BucketSize  time.Duration
	WindowCount int
}

// throughputGranularities maps the public granularity name to its config. The
// window counts mirror the overview chart's rolling windows.
var throughputGranularities = map[string]ThroughputGranularity{
	"second": {Name: "second", BucketSize: time.Second, WindowCount: 60},
	"minute": {Name: "minute", BucketSize: time.Minute, WindowCount: 60},
	"hour":   {Name: "hour", BucketSize: time.Hour, WindowCount: 24},
	"day":    {Name: "day", BucketSize: 24 * time.Hour, WindowCount: 30},
}

// ParseThroughputGranularity resolves a public granularity name (second,
// minute, hour, day) to its bucket configuration.
func ParseThroughputGranularity(name string) (ThroughputGranularity, error) {
	gran, ok := throughputGranularities[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return ThroughputGranularity{}, fmt.Errorf("invalid granularity %q, expected one of second, minute, hour, day", name)
	}
	return gran, nil
}

// ThroughputBucket is one time bucket of token volume, split into the four
// series the overview live chart stacks. Start is the bucket's inclusive start
// (UTC); the bucket spans the parent TokenThroughput's BucketSeconds.
type ThroughputBucket struct {
	Start               time.Time `json:"start"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	PromptCachedTokens  int64     `json:"prompt_cached_tokens"`
	LocallyCachedTokens int64     `json:"locally_cached_tokens"`
}

// TokenThroughput is a fixed-width window of token-volume buckets ending at the
// current time, powering the overview live-throughput chart.
type TokenThroughput struct {
	Granularity   string             `json:"granularity"`
	BucketSeconds int                `json:"bucket_seconds"`
	Buckets       []ThroughputBucket `json:"buckets"`
}

// throughputWindow returns the bucket width in seconds and the inclusive first
// and exclusive upper bucket-start unix seconds for the window ending at end.
// offset is the timezone's offset from UTC (seconds east of UTC) so buckets
// align to local boundaries — notably the "day" buckets start at local midnight,
// matching the Daily Token Usage chart instead of UTC midnight.
//
// TODO(tz): offset is the dashboard timezone's offset at `end` only (a single
// fixed value), so day/hour buckets that straddle a DST transition within the
// window can be an hour off. Acceptable for a live preview; the proper fix is to
// thread the *time.Location through GetTokenThroughput and bucket per the
// store's own zone (the perf TODO on foldThroughput would enable this for free).
func throughputWindow(gran ThroughputGranularity, end time.Time, offset int64) (bucketSeconds, first, upper int64) {
	bucketSeconds = int64(gran.BucketSize / time.Second)
	if bucketSeconds <= 0 {
		bucketSeconds = 1
	}
	current := ((end.Unix()+offset)/bucketSeconds)*bucketSeconds - offset
	first = current - int64(gran.WindowCount-1)*bucketSeconds
	upper = current + bucketSeconds
	return bucketSeconds, first, upper
}

// throughputBucketStart aligns a unix timestamp to its local bucket start, using
// the same offset math as throughputWindow. SQLite/PostgreSQL compute this in SQL
// (so they can filter and group on an index); MongoDB and tests use this helper.
func throughputBucketStart(ts, bucketSeconds, offset int64) int64 {
	return ((ts+offset)/bucketSeconds)*bucketSeconds - offset
}

// throughputAccumulator folds streamed usage rows into a fixed, zero-filled set
// of buckets so the chart can render the window directly.
type throughputAccumulator struct {
	bucketSeconds int64
	first         int64
	buckets       []ThroughputBucket
}

func newThroughputAccumulator(gran ThroughputGranularity, end time.Time, offset int64) *throughputAccumulator {
	bucketSeconds, first, _ := throughputWindow(gran, end, offset)
	buckets := make([]ThroughputBucket, gran.WindowCount)
	for i := range buckets {
		buckets[i].Start = time.Unix(first+int64(i)*bucketSeconds, 0).UTC()
	}
	return &throughputAccumulator{bucketSeconds: bucketSeconds, first: first, buckets: buckets}
}

// add folds one usage row into its bucket. A row with cacheType set
// (exact/semantic) was served from the local response cache, so all its tokens
// count as locally cached; otherwise it is a provider request whose input
// splits into uncached (+ cache writes) and prompt-cache reads — the same split
// the overview Cache Meter uses, via EntryInputSegments.
func (a *throughputAccumulator) add(bucketStartUnix int64, cacheType, provider string, inputTokens, outputTokens, totalTokens int, rawData map[string]any) {
	if a.bucketSeconds <= 0 {
		return
	}
	idx := int((bucketStartUnix - a.first) / a.bucketSeconds)
	if idx < 0 || idx >= len(a.buckets) {
		return
	}
	bucket := &a.buckets[idx]
	switch strings.ToLower(strings.TrimSpace(cacheType)) {
	case CacheTypeExact, CacheTypeSemantic:
		total := int64(totalTokens)
		if total == 0 {
			total = int64(inputTokens) + int64(outputTokens)
		}
		bucket.LocallyCachedTokens += total
	default:
		uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
			InputTokens: inputTokens,
			Provider:    provider,
			RawData:     rawData,
		})
		bucket.InputTokens += uncached + cacheWrite
		bucket.PromptCachedTokens += cached
		bucket.OutputTokens += int64(outputTokens)
	}
}

func (a *throughputAccumulator) result(gran ThroughputGranularity) *TokenThroughput {
	return &TokenThroughput{
		Granularity:   gran.Name,
		BucketSeconds: int(a.bucketSeconds),
		Buckets:       a.buckets,
	}
}

// EmptyTokenThroughput returns a zero-filled window, used when usage tracking is
// disabled so the dashboard still renders an (empty) chart.
func EmptyTokenThroughput(gran ThroughputGranularity, end time.Time, offset int64) *TokenThroughput {
	return newThroughputAccumulator(gran, end, offset).result(gran)
}

// foldThroughput scans bucketed usage rows and folds each into the accumulator,
// so the SQLite and PostgreSQL readers share one row-handling path (only the
// query/driver differs). MongoDB folds its decoded documents directly. Row
// columns, in order: bucket-start unix seconds, cache_type, input_tokens,
// output_tokens, total_tokens, provider, raw_data. The cursor is the same
// minimal interface used for the summary fold (inputSegmentRows).
//
// Why stream-and-fold instead of GROUP BY: the Input/Prompt-cached series come
// from the provider prompt-cache split, derived per row from raw_data via
// EntryInputSegments — the single source of truth for the provider-specific
// quirks (field coalescing, Anthropic additive accounting). That split is
// per-row non-linear (max/min/branch), so it can't be reconstructed from
// per-bucket sums, and we deliberately don't reimplement it in SQL across three
// backends. The query is bounded to the window by the timestamp index, matching
// the GetSummary / GetDailyUsage fold passes.
//
// TODO(perf): the overview polls this endpoint, so the coarse (hour/day) windows
// re-scan many rows per refresh. The scalable fix is to persist the
// uncached/cached/cache-write split as columns at write time (computed once via
// EntryInputSegments); summary, daily and throughput could then GROUP BY in SQL
// instead of streaming, and day/hour buckets could use the store's own timezone
// (see throughputWindow's TODO(tz)).
func foldThroughput(rows inputSegmentRows, acc *throughputAccumulator) error {
	for rows.Next() {
		var bucketStart int64
		var cacheType, rawDataJSON *string
		var inputTokens, outputTokens, totalTokens int
		var provider string
		if err := rows.Scan(&bucketStart, &cacheType, &inputTokens, &outputTokens, &totalTokens, &provider, &rawDataJSON); err != nil {
			return fmt.Errorf("failed to scan throughput row: %w", err)
		}
		var rawData map[string]any
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &rawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "error", err)
			}
		}
		cacheTypeValue := ""
		if cacheType != nil {
			cacheTypeValue = *cacheType
		}
		acc.add(bucketStart, cacheTypeValue, provider, inputTokens, outputTokens, totalTokens, rawData)
	}
	return rows.Err()
}
