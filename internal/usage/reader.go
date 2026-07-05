package usage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// UsageQueryParams specifies the query parameters for usage data retrieval.
// The optional filters (UserPath, Model, Provider, Label) apply uniformly to
// every reader method, so summaries, breakdowns, and the request log all
// describe the same filtered slice of traffic.
type UsageQueryParams struct {
	StartDate time.Time // Inclusive start (day precision)
	EndDate   time.Time // Inclusive end (day precision)
	Interval  string    // "daily", "weekly", "monthly", "yearly"
	TimeZone  string    // IANA timezone used for day-boundary interpretation and grouping
	UserPath  string    // subtree filter on tracked user path
	Model     string    // filter by exact model name (optional)
	Provider  string    // filter by provider name or provider type (optional)
	Label     string    // filter by request label, exact match (optional)
	CacheMode string    // "uncached" (default), "cached", or "all"
}

// UsageSummary holds aggregated usage statistics over a time period.
//
// The *_input_tokens split fields (uncached/cached/cache-write) are the
// provider prompt-cache breakdown of the input, summed per row via
// addInputSegments which reuses EntryInputSegments. Their sum is the
// provider-side "input parts" total; for additive-accounting providers
// (Anthropic) it exceeds TotalInput, which only sums the input_tokens column.
// Storage layers populate them by streaming rows; they are zero when the
// reader is disabled.
type UsageSummary struct {
	TotalRequests         int      `json:"total_requests"`
	TotalInput            int64    `json:"total_input_tokens"`
	TotalOutput           int64    `json:"total_output_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	UncachedInputTokens   int64    `json:"uncached_input_tokens"`
	CachedInputTokens     int64    `json:"cached_input_tokens"`
	CacheWriteInputTokens int64    `json:"cache_write_input_tokens"`
	TotalInputCost        *float64 `json:"total_input_cost"`
	TotalOutputCost       *float64 `json:"total_output_cost"`
	TotalCost             *float64 `json:"total_cost"`
	// Rewrite savings: prompt tokens request rewriters removed before the
	// provider call, and the estimated input cost avoided (nil when no
	// matched row had a priced savings estimate).
	RewriteTokensSaved int64    `json:"rewrite_tokens_saved"`
	RewriteCostSaved   *float64 `json:"rewrite_cost_saved"`
}

// addInputSegments folds one usage row's provider-cache split into the summary
// totals, reusing EntryInputSegments so every storage backend shares the single
// source of truth for the provider-specific quirks (field-name coalescing and
// the Anthropic additive-vs-subset accounting). Callers stream only the minimal
// columns; cache_type is irrelevant because the summary covers provider
// (uncached-mode) rows.
func (s *UsageSummary) addInputSegments(inputTokens int, provider string, rawData map[string]any) {
	uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
		InputTokens: inputTokens,
		Provider:    provider,
		RawData:     rawData,
	})
	s.UncachedInputTokens += uncached
	s.CachedInputTokens += cached
	s.CacheWriteInputTokens += cacheWrite
}

// inputSegmentRows is the minimal row cursor shared by the SQL backends when
// folding the provider prompt-cache split; both pgx.Rows and *sql.Rows satisfy it.
type inputSegmentRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// foldInputSegments scans (input_tokens, provider, raw_data) rows and folds each
// into the summary via addInputSegments, so the SQLite and PostgreSQL readers
// share one row-handling path (only the query/driver differs between them).
func foldInputSegments(rows inputSegmentRows, summary *UsageSummary) error {
	for rows.Next() {
		var inputTokens int
		var provider string
		var rawDataJSON *string
		if err := rows.Scan(&inputTokens, &provider, &rawDataJSON); err != nil {
			return fmt.Errorf("failed to scan usage input segment row: %w", err)
		}
		var rawData map[string]any
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &rawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "error", err)
			}
		}
		summary.addInputSegments(inputTokens, provider, rawData)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating usage input segment rows: %w", err)
	}
	return nil
}

// ModelUsage holds per-model token usage aggregates.
type ModelUsage struct {
	Model        string   `json:"model"`
	Provider     string   `json:"provider"`
	ProviderName string   `json:"provider_name,omitempty"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	InputCost    *float64 `json:"input_cost"`
	OutputCost   *float64 `json:"output_cost"`
	TotalCost    *float64 `json:"total_cost"`
}

// UserPathUsage holds per-user-path token usage aggregates.
type UserPathUsage struct {
	UserPath     string   `json:"user_path"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	InputCost    *float64 `json:"input_cost" extensions:"x-nullable"`
	OutputCost   *float64 `json:"output_cost" extensions:"x-nullable"`
	TotalCost    *float64 `json:"total_cost" extensions:"x-nullable"`
}

// LabelUsage holds per-label token usage aggregates. A request carrying
// several labels contributes its full totals to each of them, so label rows
// overlap and do not sum to the period totals.
type LabelUsage struct {
	Label        string   `json:"label"`
	Requests     int      `json:"requests"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	InputCost    *float64 `json:"input_cost" extensions:"x-nullable"`
	OutputCost   *float64 `json:"output_cost" extensions:"x-nullable"`
	TotalCost    *float64 `json:"total_cost" extensions:"x-nullable"`
}

// DailyUsage holds usage statistics for a single period.
// Date holds the period label: YYYY-MM-DD for daily, YYYY-Www for weekly,
// YYYY-MM for monthly, or YYYY for yearly intervals.
type DailyUsage struct {
	Date         string `json:"date"`
	Requests     int    `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	// Provider prompt-cache split of the period's input, folded per row from
	// raw_data (same source as the summary). Zero when the storage layer does
	// not populate them.
	UncachedInputTokens   int64    `json:"uncached_input_tokens,omitempty"`
	CachedInputTokens     int64    `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens int64    `json:"cache_write_input_tokens,omitempty"`
	InputCost             *float64 `json:"input_cost"`
	OutputCost            *float64 `json:"output_cost"`
	TotalCost             *float64 `json:"total_cost"`
}

// periodInputSplit holds the provider prompt-cache split for one time period.
type periodInputSplit struct {
	Uncached   int64
	Cached     int64
	CacheWrite int64
}

// accumulatePeriodSplit folds one row's input into the split for its period,
// reusing EntryInputSegments so every backend shares the provider-quirk logic.
func accumulatePeriodSplit(out map[string]periodInputSplit, period string, inputTokens int, provider string, rawData map[string]any) {
	uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
		InputTokens: inputTokens,
		Provider:    provider,
		RawData:     rawData,
	})
	split := out[period]
	split.Uncached += uncached
	split.Cached += cached
	split.CacheWrite += cacheWrite
	out[period] = split
}

// foldPeriodInputSegments folds (period, input_tokens, provider, raw_data) rows
// into the prompt-cache split per period. Shared by the SQL backends; MongoDB
// folds its decoded documents with accumulatePeriodSplit directly.
func foldPeriodInputSegments(rows inputSegmentRows) (map[string]periodInputSplit, error) {
	out := map[string]periodInputSplit{}
	for rows.Next() {
		var period, provider string
		var cacheType, rawDataJSON *string
		var inputTokens int
		if err := rows.Scan(&period, &cacheType, &inputTokens, &provider, &rawDataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan daily input segment row: %w", err)
		}
		// The split describes provider input; local-cache hits aren't provider
		// requests, so skip them regardless of the query's cache mode.
		if isLocalCacheType(cacheType) {
			continue
		}
		var rawData map[string]any
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &rawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "error", err)
			}
		}
		accumulatePeriodSplit(out, period, inputTokens, provider, rawData)
	}
	return out, rows.Err()
}

// isLocalCacheType reports whether a usage row was served from GoModel's local
// response cache (cache_type set), and so is not provider input.
func isLocalCacheType(cacheType *string) bool {
	if cacheType == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*cacheType)) {
	case CacheTypeExact, CacheTypeSemantic:
		return true
	default:
		return false
	}
}

// applyDailyInputSplit merges the per-period split onto matching daily rows.
func applyDailyInputSplit(daily []DailyUsage, splits map[string]periodInputSplit) {
	for i := range daily {
		if split, ok := splits[daily[i].Date]; ok {
			daily[i].UncachedInputTokens = split.Uncached
			daily[i].CachedInputTokens = split.Cached
			daily[i].CacheWriteInputTokens = split.CacheWrite
		}
	}
}

// UsageLogParams specifies query parameters for paginated usage log retrieval.
// Data filters (model, provider, label, user path) live on the embedded
// UsageQueryParams; only the log-specific view options are declared here.
type UsageLogParams struct {
	UsageQueryParams        // embed date range and data filters
	Search           string // free-text search on model/provider/request_id
	Limit            int    // page size (default 50, max 200)
	Offset           int    // pagination offset
}

// UsageLogEntry represents a single usage record in the request log.
//
// The cached_input_tokens, uncached_input_tokens, cache_write_input_tokens,
// and cached_input_ratio fields are derived from RawData at read time by
// EnrichUsageLogEntry; storage layers never populate them.
type UsageLogEntry struct {
	ID                     string         `json:"id"`
	RequestID              string         `json:"request_id"`
	ProviderID             string         `json:"provider_id"`
	Timestamp              time.Time      `json:"timestamp"`
	Model                  string         `json:"model"`
	Provider               string         `json:"provider"`
	ProviderName           string         `json:"provider_name,omitempty"`
	Endpoint               string         `json:"endpoint"`
	UserPath               string         `json:"user_path,omitempty"`
	CacheType              string         `json:"cache_type,omitempty"`
	Labels                 []string       `json:"labels,omitempty"`
	InputTokens            int            `json:"input_tokens"`
	OutputTokens           int            `json:"output_tokens"`
	TotalTokens            int            `json:"total_tokens"`
	UncachedInputTokens    int64          `json:"uncached_input_tokens,omitempty"`
	CachedInputTokens      int64          `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens  int64          `json:"cache_write_input_tokens,omitempty"`
	CachedInputRatio       float64        `json:"cached_input_ratio,omitempty"`
	InputCost              *float64       `json:"input_cost"`
	OutputCost             *float64       `json:"output_cost"`
	TotalCost              *float64       `json:"total_cost"`
	CostSource             string         `json:"cost_source,omitempty"`
	RawData                map[string]any `json:"raw_data,omitempty"`
	CostsCalculationCaveat string         `json:"costs_calculation_caveat,omitempty"`
}

// EnrichUsageLogEntry populates the derived provider-cache fields on entry
// (uncached/cached/write split and the cached ratio) from RawData. Safe to
// call on entries whose RawData does not contain provider cache fields.
func EnrichUsageLogEntry(entry *UsageLogEntry) {
	if entry == nil {
		return
	}
	uncached, cached, cacheWrite := EntryInputSegments(*entry)
	entry.UncachedInputTokens = uncached
	entry.CachedInputTokens = cached
	entry.CacheWriteInputTokens = cacheWrite
	total := uncached + cached + cacheWrite
	if total > 0 && cached > 0 {
		entry.CachedInputRatio = float64(cached) / float64(total)
	} else {
		entry.CachedInputRatio = 0
	}
}

// UsageLogResult holds a paginated list of usage log entries.
type UsageLogResult struct {
	Entries []UsageLogEntry `json:"entries"`
	Total   int             `json:"total"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
}

// RequestUsageSummary aggregates usage records that belong to one request ID.
// InputTokens and TotalTokens are normalized prompt/total counts across providers:
// cached prompt reads and cache writes are included even when the upstream provider
// reports them outside the base input token count.
type RequestUsageSummary struct {
	Entries                   int     `json:"entries"`
	InputTokens               int64   `json:"input_tokens"`
	UncachedInputTokens       int64   `json:"uncached_input_tokens"`
	CachedInputTokens         int64   `json:"cached_input_tokens"`
	CacheWriteInputTokens     int64   `json:"cache_write_input_tokens"`
	OutputTokens              int64   `json:"output_tokens"`
	TotalTokens               int64   `json:"total_tokens"`
	CachedInputRatio          float64 `json:"cached_input_ratio"`
	EstimatedCachedCharacters int64   `json:"estimated_cached_characters"`
}

// CacheOverviewSummary holds cached-only aggregate statistics over a time period.
type CacheOverviewSummary struct {
	TotalHits      int      `json:"total_hits"`
	ExactHits      int      `json:"exact_hits"`
	SemanticHits   int      `json:"semantic_hits"`
	TotalInput     int64    `json:"total_input_tokens"`
	TotalOutput    int64    `json:"total_output_tokens"`
	TotalTokens    int64    `json:"total_tokens"`
	TotalSavedCost *float64 `json:"total_saved_cost"`
}

// CacheOverviewDaily holds cached-only statistics for a single period.
type CacheOverviewDaily struct {
	Date         string   `json:"date"`
	Hits         int      `json:"hits"`
	ExactHits    int      `json:"exact_hits"`
	SemanticHits int      `json:"semantic_hits"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	SavedCost    *float64 `json:"saved_cost"`
}

// CacheOverview aggregates cached-only summary and daily series for the dashboard.
type CacheOverview struct {
	Summary CacheOverviewSummary `json:"summary"`
	Daily   []CacheOverviewDaily `json:"daily"`
}

// UsageReader provides read access to usage data for the admin API.
type UsageReader interface {
	// GetSummary returns aggregated usage statistics for the given date range.
	// If both StartDate and EndDate are zero, returns all-time statistics.
	GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error)

	// GetDailyUsage returns usage statistics grouped by the specified interval.
	// If both StartDate and EndDate are zero, returns all available data.
	GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error)

	// GetUsageByModel returns per-model token usage aggregates for the given date range.
	GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error)

	// GetUsageByUserPath returns per-user-path token usage aggregates for the given date range.
	GetUsageByUserPath(ctx context.Context, params UsageQueryParams) ([]UserPathUsage, error)

	// GetUsageByLabel returns per-label token usage aggregates for the given
	// date range. Unlabelled entries are omitted; entries with several labels
	// count once per label.
	GetUsageByLabel(ctx context.Context, params UsageQueryParams) ([]LabelUsage, error)

	// GetUsageLog returns a paginated list of individual usage entries with optional filtering.
	GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error)

	// GetUsageByRequestIDs returns usage log entries grouped by request_id.
	// Missing IDs are omitted from the returned map.
	GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error)

	// GetCacheOverview returns cached-only aggregates for the admin dashboard.
	GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error)

	// GetTokenThroughput returns a fixed-width window of token-volume buckets
	// (input/output/prompt-cached/locally-cached) ending at end, for the
	// overview live-throughput chart. The window is global (not user-path
	// scoped). offset is the request timezone's offset from UTC in seconds, so
	// buckets align to local boundaries (e.g. day buckets at local midnight).
	GetTokenThroughput(ctx context.Context, gran ThroughputGranularity, end time.Time, offset int64) (*TokenThroughput, error)
}

func displayUsageProviderName(providerName, provider string) string {
	if trimmed := strings.TrimSpace(providerName); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(provider)
}

func compactNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	compacted := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		compacted = append(compacted, trimmed)
	}
	return compacted
}
