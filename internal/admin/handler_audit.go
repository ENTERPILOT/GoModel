package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// maxAuditLogLimit caps the page size accepted by the audit log endpoint and
// matches the value documented in the @Param limit annotation below.
const maxAuditLogLimit = 100

// defaultAuditLogLimit is the effective page size when the caller omits limit.
// It mirrors the reader's pagination default so the disabled-reader fast path
// reports the same limit an enabled reader would.
const defaultAuditLogLimit = 25

// conversationBuildTimeout bounds the store lookups behind the conversation
// endpoint. The linkage fallback returns the partial thread if it expires.
const conversationBuildTimeout = 10 * time.Second

// AuditLog handles GET /admin/audit/log
//
// @Summary      Get paginated audit log entries
// @Description  Entries are slimmed for the wire: request/response bodies,
// @Description  per-attempt error bodies, and rewritten revision bodies are
// @Description  omitted (bodies_omitted=true). Fetch /admin/audit/detail for
// @Description  the full payload of one entry.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days         query     int     false  "Number of days (default 30)"
// @Param        start_date   query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date     query     string  false  "End date (YYYY-MM-DD)"
// @Param        requested_model  query     string  false  "Filter by requested model selector"
// @Param        provider     query     string  false  "Filter by provider name or provider type"
// @Param        method       query     string  false  "Filter by HTTP method"
// @Param        path         query     string  false  "Filter by request path"
// @Param        user_path    query     string  false  "Filter by tracked user path subtree"
// @Param        session_id   query     string  false  "Filter by exact session id"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        search       query     string  false  "Search across request_id/requested_model/provider/method/path/session_id/error_type/error_message"
// @Param        limit        query     int     false  "Page size (default 25, max 100)"
// @Param        offset       query     int     false  "Offset for pagination"
// @Success      200  {object}  auditLogListResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/audit/log [get]
func (h *Handler) AuditLog(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of whether audit
	// logging is configured.
	params, err := parseAuditLogQueryParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if h.auditReader == nil {
		// Echo the effective pagination so the response matches the enabled-reader
		// contract. Returning limit:0 here would make the client send limit=0 on
		// its next request, which fails validation above with a 400.
		limit := params.Limit
		if limit <= 0 {
			limit = defaultAuditLogLimit
		}
		return c.JSON(http.StatusOK, auditLogListResponse{
			Entries: []auditLogEntryResponse{},
			Limit:   limit,
			Offset:  params.Offset,
		})
	}

	result, err := h.auditReader.GetLogs(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &auditlog.LogListResult{Entries: []auditlog.LogEntry{}}
	}
	if result.Entries == nil {
		result.Entries = []auditlog.LogEntry{}
	}

	response, err := h.auditLogResponse(c.Request().Context(), result)
	if err != nil {
		return handleError(c, err)
	}
	// The list ships scalar metadata only; full payloads stay behind
	// /admin/audit/detail (see slimAuditListEntry).
	for i := range response.Entries {
		slimAuditListEntry(&response.Entries[i])
	}
	return c.JSON(http.StatusOK, response)
}

// parseAuditLogQueryParams parses and validates the shared audit log filter,
// search, and pagination query parameters.
//
// A session_id filter without explicit date parameters queries the whole
// session rather than the default trailing window: the thread view must show
// every request of a session regardless of the date range the list was
// browsed with, and the result set is already bounded by the session id.
func parseAuditLogQueryParams(c *echo.Context) (auditlog.LogQueryParams, error) {
	var params auditlog.LogQueryParams

	sessionID := strings.TrimSpace(c.QueryParam("session_id"))
	explicitDates := c.QueryParam("days") != "" ||
		strings.TrimSpace(c.QueryParam("start_date")) != "" ||
		strings.TrimSpace(c.QueryParam("end_date")) != ""
	var dates auditlog.QueryParams
	if sessionID == "" || explicitDates {
		dateRange, err := parseDateRangeParams(c)
		if err != nil {
			return params, err
		}
		dates = auditlog.QueryParams{
			StartDate: dateRange.StartDate,
			EndDate:   dateRange.EndDate,
		}
	}
	userPath, err := normalizeUserPathQueryParam("user_path", c.QueryParam("user_path"))
	if err != nil {
		return params, err
	}

	requestedModel := c.QueryParam("requested_model")
	if requestedModel == "" {
		requestedModel = c.QueryParam("model")
	}

	params = auditlog.LogQueryParams{
		QueryParams:    dates,
		RequestedModel: requestedModel,
		Provider:       c.QueryParam("provider"),
		Method:         strings.ToUpper(c.QueryParam("method")),
		Path:           c.QueryParam("path"),
		UserPath:       userPath,
		SessionID:      sessionID,
		ErrorType:      c.QueryParam("error_type"),
		Search:         c.QueryParam("search"),
	}

	if sc := c.QueryParam("status_code"); sc != "" {
		parsed, err := strconv.Atoi(sc)
		if err != nil {
			return params, core.NewInvalidRequestError("invalid status_code, expected integer", nil)
		}
		params.StatusCode = &parsed
	}

	if stream := c.QueryParam("stream"); stream != "" {
		parsed, err := strconv.ParseBool(stream)
		if err != nil {
			return params, core.NewInvalidRequestError("invalid stream value, expected true or false", nil)
		}
		params.Stream = &parsed
	}

	if l := c.QueryParam("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			return params, core.NewInvalidRequestError("invalid limit, expected positive integer", nil)
		}
		if parsed > maxAuditLogLimit {
			return params, core.NewInvalidRequestError("invalid limit parameter: limit must be between 1 and 100", nil)
		}
		params.Limit = parsed
	}
	if o := c.QueryParam("offset"); o != "" {
		parsed, err := strconv.Atoi(o)
		if err != nil || parsed < 0 {
			return params, core.NewInvalidRequestError("invalid offset, expected non-negative integer", nil)
		}
		params.Offset = parsed
	}
	return params, nil
}

// AuditSessions handles GET /admin/audit/sessions
//
// @Summary      Get paginated audit sessions (threads)
// @Description  Groups audit log entries by session id into threads and returns
// @Description  one summary per thread — its latest entry, entry count, and time
// @Description  span — ordered by latest activity. Entries without a session id
// @Description  appear as single-entry threads. Filters apply to entries before
// @Description  grouping.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days         query     int     false  "Number of days (default 30)"
// @Param        start_date   query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date     query     string  false  "End date (YYYY-MM-DD)"
// @Param        requested_model  query     string  false  "Filter by requested model selector"
// @Param        provider     query     string  false  "Filter by provider name or provider type"
// @Param        method       query     string  false  "Filter by HTTP method"
// @Param        path         query     string  false  "Filter by request path"
// @Param        user_path    query     string  false  "Filter by tracked user path subtree"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        search       query     string  false  "Search across request_id/requested_model/provider/method/path/session_id/error_type/error_message"
// @Param        limit        query     int     false  "Page size in threads (default 25, max 100)"
// @Param        offset       query     int     false  "Offset for pagination"
// @Success      200  {object}  auditSessionsListResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/audit/sessions [get]
func (h *Handler) AuditSessions(c *echo.Context) error {
	params, err := parseAuditLogQueryParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if h.auditReader == nil {
		limit := params.Limit
		if limit <= 0 {
			limit = defaultAuditLogLimit
		}
		return c.JSON(http.StatusOK, auditSessionsListResponse{
			Sessions: []auditSessionResponse{},
			Limit:    limit,
			Offset:   params.Offset,
		})
	}

	result, err := h.auditReader.GetSessions(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &auditlog.SessionListResult{}
	}

	// Reuse the entry response builder for usage enrichment of the latest entries.
	latest := make([]auditlog.LogEntry, len(result.Sessions))
	for i := range result.Sessions {
		latest[i] = result.Sessions[i].Latest
	}
	enriched, err := h.auditLogResponse(c.Request().Context(), &auditlog.LogListResult{Entries: latest})
	if err != nil {
		return handleError(c, err)
	}

	response := auditSessionsListResponse{
		Sessions: make([]auditSessionResponse, len(result.Sessions)),
		Total:    result.Total,
		Limit:    result.Limit,
		Offset:   result.Offset,
	}
	for i, session := range result.Sessions {
		// Session heads are list rows too. Keep the default grouped view on the
		// same slim payload contract as /admin/audit/log.
		slimAuditListEntry(&enriched.Entries[i])
		response.Sessions[i] = auditSessionResponse{
			SessionID:      session.SessionID,
			Count:          session.Count,
			FirstTimestamp: session.FirstTimestamp,
			LastTimestamp:  session.LastTimestamp,
			Latest:         enriched.Entries[i],
		}
	}
	return c.JSON(http.StatusOK, response)
}

func (h *Handler) auditLogResponse(ctx context.Context, result *auditlog.LogListResult) (*auditLogListResponse, error) {
	if result == nil {
		return &auditLogListResponse{Entries: []auditLogEntryResponse{}}, nil
	}

	response := &auditLogListResponse{
		Entries: make([]auditLogEntryResponse, len(result.Entries)),
		Total:   result.Total,
		Limit:   result.Limit,
		Offset:  result.Offset,
	}
	for i := range result.Entries {
		response.Entries[i].LogEntry = result.Entries[i]
	}

	requestIDs := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		requestIDs = append(requestIDs, entry.RequestID)
	}

	summaries, err := usage.SummarizeUsageForRequestIDs(ctx, h.usageReader, requestIDs)
	if err != nil {
		slog.Warn("failed to enrich audit log entries with usage", "error", err, "request_count", len(requestIDs))
		return response, nil
	}
	for i := range response.Entries {
		requestID := response.Entries[i].RequestID
		if summary, ok := summaries[requestID]; ok {
			response.Entries[i].Usage = summary
		}
	}

	return response, nil
}

// auditStatsHourlyRangeDays is the largest date-range span (in days) that
// still gets hourly buckets; longer ranges fall back to daily buckets so the
// chart stays readable.
const auditStatsHourlyRangeDays = 3

// AuditStats handles GET /admin/audit/stats
//
// @Summary      Get time-bucketed request status and latency stats
// @Description  Returns request counts grouped into 2xx/4xx/5xx status classes
// @Description  per time bucket, an overall success-rate summary, and average
// @Description  request duration per provider for the dashboard charts.
// @Description  Ranges up to 3 days use hourly buckets, longer ranges daily.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Success      200  {object}  auditlog.RequestStats
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/audit/stats [get]
func (h *Handler) AuditStats(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of whether audit
	// logging is configured.
	dateRange, err := parseDateRangeParams(c)
	if err != nil {
		return handleError(c, err)
	}

	interval := auditlog.StatsIntervalDay
	if dateRange.EndDate.Sub(dateRange.StartDate) < auditStatsHourlyRangeDays*24*time.Hour {
		interval = auditlog.StatsIntervalHour
	}

	if h.auditReader == nil {
		return c.JSON(http.StatusOK, auditlog.EmptyRequestStats(interval))
	}

	_, location := dashboardTimeZone(c)
	params := auditlog.RequestStatsParams{
		QueryParams: auditlog.QueryParams{
			StartDate: dateRange.StartDate,
			EndDate:   dateRange.EndDate,
		},
		Interval: interval,
		Location: location,
		Now:      timeNow(),
	}

	stats, err := h.auditReader.GetRequestStats(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if stats == nil {
		stats = auditlog.EmptyRequestStats(interval)
	}
	return c.JSON(http.StatusOK, stats)
}

// AuditLogDetail handles GET /admin/audit/detail.
//
// @Summary      Get audit log entry detail
// @Description  Returns one audit log entry enriched with usage summary when available.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        log_id  query     string  true  "Audit log entry ID"
// @Success      200  {object}  auditLogEntryResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      500  {object}  core.GatewayError
// @Router       /admin/audit/detail [get]
func (h *Handler) AuditLogDetail(c *echo.Context) error {
	logID := strings.TrimSpace(c.QueryParam("log_id"))
	if logID == "" {
		return handleError(c, core.NewInvalidRequestError("log_id is required", nil))
	}
	if h.auditReader == nil {
		return handleError(c, featureUnavailableError("audit log detail is unavailable"))
	}

	entry, err := h.auditReader.GetLogByID(c.Request().Context(), logID)
	if err != nil {
		return handleError(c, err)
	}
	if entry == nil {
		return handleError(c, core.NewNotFoundError("audit log not found: "+logID))
	}

	response, err := h.auditLogResponse(c.Request().Context(), &auditlog.LogListResult{
		Entries: []auditlog.LogEntry{*entry},
		Total:   1,
		Limit:   1,
	})
	if err != nil {
		return handleError(c, err)
	}
	if len(response.Entries) == 0 {
		return handleError(c, core.NewNotFoundError("audit log not found: "+logID))
	}
	return c.JSON(http.StatusOK, response.Entries[0])
}

// AuditConversation handles GET /admin/audit/conversation
//
// @Summary      Get the interaction session containing an audit log entry
// @Description  Thread entries carry the request/response bodies the
// @Description  transcript is built from; attempts, request revisions, and
// @Description  response headers are omitted; redacted request headers and
// @Description  per-request usage summaries are retained for continuation
// @Description  and prompt-cache visualization.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        log_id  query     string  true   "Anchor audit log entry ID"
// @Param        limit   query     int     false  "Max entries in thread (default 40, max 200)"
// @Success      200  {object}  auditConversationResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/audit/conversation [get]
func (h *Handler) AuditConversation(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for missing/invalid params, regardless of whether
	// audit logging is configured.
	logID := strings.TrimSpace(c.QueryParam("log_id"))
	if logID == "" {
		return handleError(c, core.NewInvalidRequestError("log_id is required", nil))
	}

	limit := 40
	if l := c.QueryParam("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil {
			return handleError(c, core.NewInvalidRequestError("invalid limit, expected integer", nil))
		}
		if parsed < 1 || parsed > 200 {
			return handleError(c, core.NewInvalidRequestError("invalid limit parameter: limit must be between 1 and 200", nil))
		}
		limit = parsed
	}

	if h.auditReader == nil {
		return c.JSON(http.StatusOK, auditConversationResponse{
			AnchorID: logID,
			Entries:  []auditLogEntryResponse{},
		})
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), conversationBuildTimeout)
	defer cancel()
	result, err := h.auditReader.GetConversation(ctx, logID, limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return handleError(c, core.NewProviderError("", http.StatusGatewayTimeout,
				"audit conversation lookup timed out", err))
		}
		return handleError(c, err)
	}
	if result == nil {
		result = &auditlog.ConversationResult{
			AnchorID: logID,
			Entries:  []auditlog.LogEntry{},
		}
	}
	if result.Entries == nil {
		result.Entries = []auditlog.LogEntry{}
	}
	enriched, err := h.auditLogResponse(ctx, &auditlog.LogListResult{Entries: result.Entries})
	if err != nil {
		return handleError(c, err)
	}
	for i := range enriched.Entries {
		slimConversationEntry(&enriched.Entries[i].LogEntry)
	}

	return c.JSON(http.StatusOK, auditConversationResponse{
		AnchorID:  result.AnchorID,
		Entries:   enriched.Entries,
		Truncated: result.Truncated,
	})
}
