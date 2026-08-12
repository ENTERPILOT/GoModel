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
	RequestedModel string
	Provider       string // filter by provider name or provider type
	Method         string
	Path           string
	UserPath       string
	SessionID      string // exact-match session id filter
	ErrorType      string
	Search         string
	StatusCode     *int
	Stream         *bool
	Limit          int
	Offset         int
	// OmitAttempts excludes provider attempts from returned entries. The default is false.
	OmitAttempts bool
	// ExactUserPath matches only UserPath instead of its subtree. The default is false.
	// An exact root path also matches legacy empty and null stored paths.
	ExactUserPath bool
	// Internal keyset cursor used while assembling multi-page conversations.
	beforeTimestamp time.Time
	beforeID        string
}

// LogListResult holds a paginated list of audit log entries.
type LogListResult struct {
	Entries []LogEntry `json:"entries"`
	Total   int        `json:"total"`
	Limit   int        `json:"limit"`
	Offset  int        `json:"offset"`
}

// SessionSummary describes one session (thread) of audit log entries: its
// latest matching entry plus aggregate span and counts. Count reflects the
// active filters; TotalCount covers the complete session. Entries without a
// session id form singleton threads whose SessionID is empty.
type SessionSummary struct {
	SessionID      string    `json:"session_id,omitempty"`
	Count          int       `json:"count"`
	TotalCount     int       `json:"total_count"`
	FirstTimestamp time.Time `json:"first_timestamp"`
	LastTimestamp  time.Time `json:"last_timestamp"`
	Latest         LogEntry  `json:"latest"`
}

// SessionListResult holds a paginated list of session summaries ordered by
// latest activity.
type SessionListResult struct {
	Sessions []SessionSummary `json:"sessions"`
	Total    int              `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

// ConversationResult holds a conversation thread with a selected anchor log.
type ConversationResult struct {
	AnchorID string     `json:"anchor_id"`
	Entries  []LogEntry `json:"entries"`

	// Truncated reports that more session or linkage entries exist than were
	// returned.
	Truncated bool `json:"truncated,omitempty"`
}

// InteractionParent contains only the persisted identity needed to attach a
// dashboard continuation to its parent session.
type InteractionParent struct {
	UserPath  string
	SessionID string
}

// Reader provides read access to audit log data for the admin API.
type Reader interface {
	// GetLogs returns a paginated list of audit log entries with optional filtering.
	GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error)

	// GetSessions returns a paginated list of audit sessions (threads): one
	// summary per distinct session id, plus singleton threads for entries
	// without one, ordered by latest activity. Filters apply to entries before
	// grouping, so a thread's Latest, Count and span reflect the matching
	// entries. TotalCount reflects the complete session.
	GetSessions(ctx context.Context, params LogQueryParams) (*SessionListResult, error)

	// GetLogByID returns a single audit log entry by ID.
	// Returns (nil, nil) when no entry exists for the given ID.
	GetLogByID(ctx context.Context, id string) (*LogEntry, error)

	// GetInteractionParent returns the fields needed to authorize and inherit a
	// dashboard continuation without loading captured bodies or attempts.
	// SQL and MongoDB readers return (nil, nil) when no entry exists; session
	// capture treats that result as no parent.
	GetInteractionParent(ctx context.Context, id string) (*InteractionParent, error)

	// GetConversation returns the anchor's session when it has one. Entries
	// without a detected session fall back to Responses API linkage fields:
	// request_body.previous_response_id and response_body.id.
	GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error)

	// GetRequestStats returns time-bucketed status-class counts and
	// per-provider latency aggregates for the dashboard charts.
	GetRequestStats(ctx context.Context, params RequestStatsParams) (*RequestStats, error)
}
