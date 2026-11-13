package auditlog

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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
	RequestID      string // exact-match request id filter
	// UserPathPrefix matches user paths that start with this text, so
	// "/team/al" matches "/team/alpha/bot". Unlike UserPath it does not stop at
	// segment boundaries, which suits type-ahead filtering. A missing leading
	// "/" is added. Case-sensitive, so an index serves it.
	UserPathPrefix string
	// ModelPrefix matches entries whose requested or resolved model starts
	// with this text. Resolved models are stored provider-qualified, e.g.
	// "openai/gpt-5". Case-sensitive, so an index serves it.
	ModelPrefix string
	ErrorType   string
	Search      string
	StatusCode  *int
	Stream      *bool
	// ExcludeOperations drops entries whose path belongs to one of these
	// operations. Entries outside every operation (e.g. authentication
	// events) always stay.
	ExcludeOperations []core.Operation
	Limit             int
	Offset            int
	// OmitAttempts excludes provider attempts from returned entries. The default is false.
	OmitAttempts bool
	// OmitBodies leaves the request and response bodies and each provider
	// attempt's response body in the database, for list views that show
	// metadata only. Each body that was captured comes back as an
	// OmittedBody. The default is false.
	OmitBodies bool
	// ExactUserPath matches only UserPath instead of its subtree. The default is false.
	// An exact root path also matches legacy empty and null stored paths.
	ExactUserPath bool
	// Internal keyset cursor used while assembling multi-page conversations.
	beforeTimestamp time.Time
	beforeID        string
}

// OmittedBody stands in for a captured body that a query with
// LogQueryParams.OmitBodies left in the database. Conversation reports that
// the request or response body is shaped like a conversation (see
// admin.hasConversationPayload, which the readers' queries mirror).
type OmittedBody struct {
	Conversation bool `json:"-"`
}

// omittedBodies records, for one row read with OmitBodies, which bodies the
// query left out and whether each is shaped like a conversation.
type omittedBodies struct {
	Request              bool `bson:"_request_body_captured"`
	Response             bool `bson:"_response_body_captured"`
	RequestConversation  bool `bson:"_request_conversation"`
	ResponseConversation bool `bson:"_response_conversation"`
}

// apply puts an OmittedBody where each left-out body was.
func (o omittedBodies) apply(entry *LogEntry) {
	if entry == nil || entry.Data == nil {
		return
	}
	if o.Request {
		entry.Data.RequestBody = OmittedBody{Conversation: o.RequestConversation}
	}
	if o.Response {
		entry.Data.ResponseBody = OmittedBody{Conversation: o.ResponseConversation}
	}
}

// LogListResult holds a paginated list of audit log entries.
type LogListResult struct {
	Entries []LogEntry `json:"entries"`
	Total   int        `json:"total"`
	Limit   int        `json:"limit"`
	Offset  int        `json:"offset"`
}

// SessionSummary describes one session (thread) of audit log entries. Filters
// choose the latest matching entry; RequestCount covers the complete session.
// Entries without a session id form singleton threads.
type SessionSummary struct {
	SessionID    string   `json:"session_id,omitempty"`
	RequestCount int      `json:"request_count"`
	Latest       LogEntry `json:"latest"`
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
	// grouping. RequestCount reflects the complete session.
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

	// GetLastUsedByAuthKeys returns the newest audit entry timestamp per auth
	// key id. Keys without audit entries are absent from the result; an empty
	// keyIDs list returns an empty map without querying.
	GetLastUsedByAuthKeys(ctx context.Context, keyIDs []string) (map[string]time.Time, error)
}
