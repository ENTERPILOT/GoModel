package usage

import (
	"context"
	"time"
)

// UsageQueryParams specifies the query parameters for usage data retrieval.
type UsageQueryParams struct {
	StartDate time.Time // Inclusive start (day precision)
	EndDate   time.Time // Inclusive end (day precision)
	Interval  string    // "daily", "weekly", "monthly", "yearly"
	TimeZone  string    // IANA timezone used for day-boundary interpretation and grouping
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
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	InputCost    *float64 `json:"input_cost"`
	OutputCost   *float64 `json:"output_cost"`
	TotalCost    *float64 `json:"total_cost"`
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
	Provider         string // filter by provider (optional)
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
	Endpoint               string         `json:"endpoint"`
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

	// GetUsageLog returns a paginated list of individual usage entries with optional filtering.
	GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error)
}
