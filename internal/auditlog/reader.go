package auditlog

import (
	"context"
	"time"
)

// QueryParams specifies the date range for audit log retrieval.
type QueryParams struct {
	StartDate time.Time // Inclusive start (day precision)
	EndDate   time.Time // Inclusive end (day precision)
}

// LogQueryParams specifies query parameters for paginated audit log retrieval.
type LogQueryParams struct {
	QueryParams
	Model      string
	Provider   string
	Method     string
	Path       string
	UserPath   string
	ErrorType  string
	Search     string
	StatusCode *int
	Stream     *bool
	Limit      int
	Offset     int
}

// LogListResult holds a paginated list of audit log entries.
type LogListResult struct {
	Entries []LogEntry `json:"entries"`
	Total   int        `json:"total"`
	Limit   int        `json:"limit"`
	Offset  int        `json:"offset"`
}

// ConversationResult holds a linear conversation thread centered around an anchor log.
type ConversationResult struct {
	AnchorID string     `json:"anchor_id"`
	Entries  []LogEntry `json:"entries"`
}

// Reader provides read access to audit log data for the admin API.
type Reader interface {
	// GetLogs returns a paginated list of audit log entries with optional filtering.
	GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error)

	// GetLogByID returns a single audit log entry by ID.
	// Returns (nil, nil) when no entry exists for the given ID.
	GetLogByID(ctx context.Context, id string) (*LogEntry, error)

	// GetConversation returns a linear conversation thread around a seed log entry.
	// It follows Responses API linkage fields when available:
	// request_body.previous_response_id and response_body.id.
	GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error)
}
