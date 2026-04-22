package usage

import (
	"context"
	"strings"
	"time"
)

// UsageQueryParams specifies the query parameters for usage data retrieval.
type UsageQueryParams struct {
	StartDate time.Time // Inclusive start (day precision)
	EndDate   time.Time // Inclusive end (day precision)
	Interval  string    // "daily", "weekly", "monthly", "yearly"
	TimeZone  string    // IANA timezone used for day-boundary interpretation and grouping
	UserPath  string    // subtree filter on tracked user path
	CacheMode string    // "uncached" (default), "cached", or "all"
}

// UsageSummary holds aggregated usage statistics over a time period.
type UsageSummary struct {
	TotalRequests   int      `json:"total_requests"`
	TotalInput      int64    `json:"total_input_tokens"`
	TotalOutput     int64    `json:"total_output_tokens"`
	TotalTokens     int64    `json:"total_tokens"`
	TotalInputCost  *float64 `json:"total_input_cost"`
	TotalOutputCost *float64 `json:"total_output_cost"`
	TotalCost       *float64 `json:"total_cost"`
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

// DailyUsage holds usage statistics for a single period.
// Date holds the period label: YYYY-MM-DD for daily, YYYY-Www for weekly,
// YYYY-MM for monthly, or YYYY for yearly intervals.
type DailyUsage struct {
	Date         string   `json:"date"`
	Requests     int      `json:"requests"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	InputCost    *float64 `json:"input_cost"`
	OutputCost   *float64 `json:"output_cost"`
	TotalCost    *float64 `json:"total_cost"`
}

// UsageLogParams specifies query parameters for paginated usage log retrieval.
type UsageLogParams struct {
	UsageQueryParams        // embed date range
	Model            string // filter by model (optional)
	Provider         string // filter by provider name or provider type (optional)
	Search           string // free-text search on model/provider/request_id
	Limit            int    // page size (default 50, max 200)
	Offset           int    // pagination offset
}

// UsageLogEntry represents a single usage record in the request log.
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
	InputTokens            int            `json:"input_tokens"`
	OutputTokens           int            `json:"output_tokens"`
	TotalTokens            int            `json:"total_tokens"`
	InputCost              *float64       `json:"input_cost"`
	OutputCost             *float64       `json:"output_cost"`
	TotalCost              *float64       `json:"total_cost"`
	RawData                map[string]any `json:"raw_data,omitempty"`
	CostsCalculationCaveat string         `json:"costs_calculation_caveat,omitempty"`
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

	// GetUsageLog returns a paginated list of individual usage entries with optional filtering.
	GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error)

	// GetUsageByRequestIDs returns usage log entries grouped by request_id.
	// Missing IDs are omitted from the returned map.
	GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error)

	// GetCacheOverview returns cached-only aggregates for the admin dashboard.
	GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error)
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
