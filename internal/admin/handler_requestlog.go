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

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

// maxRequestLogLimit caps the page size accepted by the request log endpoint and
// matches the value documented in the @Param limit annotation below.
const maxRequestLogLimit = 100

// defaultRequestLogLimit is the effective page size when the caller omits limit.
// It mirrors the reader's pagination default so the disabled-reader fast path
// reports the same limit an enabled reader would.
const defaultRequestLogLimit = 25

// conversationBuildTimeout bounds the store lookups behind the conversation
// endpoint. The linkage fallback returns the partial thread if it expires.
const conversationBuildTimeout = 10 * time.Second

// RequestLog handles GET /admin/request-logs
//
// @Summary      Get paginated request log entries
// @Description  Entries are slimmed for the wire: request/response bodies,
// @Description  per-attempt error bodies, and rewritten revision bodies are
// @Description  omitted (bodies_omitted=true). Fetch /admin/request-logs/detail for
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
// @Param        user_path_prefix  query  string  false  "Filter by user paths starting with this text (case-sensitive, index-served)"
// @Param        model_prefix  query    string  false  "Filter by requested or resolved model starting with this text (case-sensitive, index-served)"
// @Param        request_id   query     string  false  "Filter by exact request id"
// @Param        session_id   query     string  false  "Filter by exact session id"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        exclude_operation  query  string  false  "Comma-separated endpoint operations to hide, e.g. mcp,provider_passthrough,audio_speech; other entries, including unclassified ones, stay"
// @Param        search       query     string  false  "Search across request_id/requested_model/provider/method/path/session_id/error_type/error_message"
// @Param        limit        query     int     false  "Page size (default 25, max 100)"
// @Param        offset       query     int     false  "Offset for pagination"
// @Success      200  {object}  requestLogListResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Router       /admin/request-logs [get]
func (h *Handler) RequestLog(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of whether request
	// logging is configured.
	params, err := parseRequestLogQueryParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if h.requestLogReader == nil {
		// Echo the effective pagination so the response matches the enabled-reader
		// contract. Returning limit:0 here would make the client send limit=0 on
		// its next request, which fails validation above with a 400.
		limit := params.Limit
		if limit <= 0 {
			limit = defaultRequestLogLimit
		}
		return c.JSON(http.StatusOK, requestLogListResponse{
			Entries: []requestLogEntryResponse{},
			Limit:   limit,
			Offset:  params.Offset,
		})
	}

	asOf := time.Now().UnixMicro()
	result, err := h.requestLogReader.GetLogs(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &requestlog.LogListResult{Entries: []requestlog.LogEntry{}}
	}
	if result.Entries == nil {
		result.Entries = []requestlog.LogEntry{}
	}

	response, err := h.requestLogResponse(c.Request().Context(), result)
	if err != nil {
		return handleError(c, err)
	}
	// The list ships scalar metadata only; full payloads stay behind
	// /admin/request-logs/detail (see slimRequestLogListEntry).
	for i := range response.Entries {
		slimRequestLogListEntry(&response.Entries[i])
	}
	response.AsOf = asOf
	return c.JSON(http.StatusOK, response)
}

// parseRequestLogQueryParams parses and validates the shared request log filter,
// search, and pagination query parameters.
//
// A session_id filter without explicit date parameters queries the whole
// session rather than the default trailing window: the thread view must show
// every request of a session regardless of the date range the list was
// browsed with, and the result set is already bounded by the session id.
func parseRequestLogQueryParams(c *echo.Context) (requestlog.LogQueryParams, error) {
	var params requestlog.LogQueryParams

	sessionID := strings.TrimSpace(c.QueryParam("session_id"))
	explicitDates := c.QueryParam("days") != "" ||
		strings.TrimSpace(c.QueryParam("start_date")) != "" ||
		strings.TrimSpace(c.QueryParam("end_date")) != ""
	var dates requestlog.QueryParams
	if sessionID == "" || explicitDates {
		dateRange, err := parseDateRangeParams(c)
		if err != nil {
			return params, err
		}
		dates = requestlog.QueryParams{
			StartDate: dateRange.StartDate,
			EndDate:   dateRange.EndDate,
		}
	}
	userPath, err := scopedUserPath(c, "user_path", c.QueryParam("user_path"))
	if err != nil {
		return params, err
	}

	requestedModel := c.QueryParam("requested_model")
	if requestedModel == "" {
		requestedModel = c.QueryParam("model")
	}

	params = requestlog.LogQueryParams{
		QueryParams:    dates,
		RequestedModel: requestedModel,
		Provider:       c.QueryParam("provider"),
		Method:         strings.ToUpper(c.QueryParam("method")),
		Path:           c.QueryParam("path"),
		UserPath:       userPath,
		SessionID:      sessionID,
		RequestID:      strings.TrimSpace(c.QueryParam("request_id")),
		UserPathPrefix: c.QueryParam("user_path_prefix"),
		ModelPrefix:    c.QueryParam("model_prefix"),
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

	if raw := c.QueryParam("exclude_operation"); raw != "" {
		ops, unknown, ok := core.ParseOperations(raw)
		if !ok {
			return params, core.NewInvalidRequestError("invalid exclude_operation: "+unknown, nil)
		}
		params.ExcludeOperations = ops
	}

	if l := c.QueryParam("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			return params, core.NewInvalidRequestError("invalid limit, expected positive integer", nil)
		}
		if parsed > maxRequestLogLimit {
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

// RequestLogSessions handles GET /admin/request-logs/sessions
//
// @Summary      Get paginated request log sessions (threads)
// @Description  Groups request log entries by session id into threads and returns
// @Description  one summary per thread — its latest matching entry and complete
// @Description  request count — ordered by latest activity. Entries without a session id
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
// @Param        user_path_prefix  query  string  false  "Filter by user paths starting with this text (case-sensitive, index-served)"
// @Param        model_prefix  query    string  false  "Filter by requested or resolved model starting with this text (case-sensitive, index-served)"
// @Param        request_id   query     string  false  "Filter by exact request id"
// @Param        session_id   query     string  false  "Filter by exact session id"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        exclude_operation  query  string  false  "Comma-separated endpoint operations to hide, e.g. mcp,provider_passthrough,audio_speech; other entries, including unclassified ones, stay"
// @Param        search       query     string  false  "Search across request_id/requested_model/provider/method/path/session_id/error_type/error_message"
// @Param        limit        query     int     false  "Page size in threads (default 25, max 100)"
// @Param        offset       query     int     false  "Offset for pagination"
// @Success      200  {object}  requestLogSessionsListResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Router       /admin/request-logs/sessions [get]
func (h *Handler) RequestLogSessions(c *echo.Context) error {
	params, err := parseRequestLogQueryParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if h.requestLogReader == nil {
		limit := params.Limit
		if limit <= 0 {
			limit = defaultRequestLogLimit
		}
		return c.JSON(http.StatusOK, requestLogSessionsListResponse{
			Sessions: []requestLogSessionResponse{},
			Limit:    limit,
			Offset:   params.Offset,
		})
	}

	asOf := time.Now().UnixMicro()
	result, err := h.requestLogReader.GetSessions(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &requestlog.SessionListResult{}
	}

	// Reuse the entry response builder for usage enrichment of the latest entries.
	latest := make([]requestlog.LogEntry, len(result.Sessions))
	for i := range result.Sessions {
		latest[i] = result.Sessions[i].Latest
	}
	enriched, err := h.requestLogResponse(c.Request().Context(), &requestlog.LogListResult{Entries: latest})
	if err != nil {
		return handleError(c, err)
	}

	response := requestLogSessionsListResponse{
		Sessions: make([]requestLogSessionResponse, len(result.Sessions)),
		Total:    result.Total,
		Limit:    result.Limit,
		Offset:   result.Offset,
		AsOf:     asOf,
	}
	for i, session := range result.Sessions {
		// Session heads are list rows too. Keep the default grouped view on the
		// same slim payload contract as /admin/request-logs.
		slimRequestLogListEntry(&enriched.Entries[i])
		response.Sessions[i] = requestLogSessionResponse{
			RequestCount: session.RequestCount,
			Latest:       enriched.Entries[i],
		}
	}
	return c.JSON(http.StatusOK, response)
}

func (h *Handler) requestLogResponse(ctx context.Context, result *requestlog.LogListResult) (*requestLogListResponse, error) {
	if result == nil {
		return &requestLogListResponse{Entries: []requestLogEntryResponse{}}, nil
	}

	response := &requestLogListResponse{
		Entries: make([]requestLogEntryResponse, len(result.Entries)),
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
		slog.Warn("failed to enrich request log entries with usage", "error", err, "request_count", len(requestIDs))
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

// requestLogStatsHourlyRangeDays is the largest date-range span (in days) that
// still gets hourly buckets; longer ranges fall back to daily buckets so the
// chart stays readable.
const requestLogStatsHourlyRangeDays = 3

// RequestLogStats handles GET /admin/request-logs/stats
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
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Success      200  {object}  requestlog.RequestStats
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Router       /admin/request-logs/stats [get]
func (h *Handler) RequestLogStats(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of whether request
	// logging is configured.
	dateRange, err := parseDateRangeParams(c)
	if err != nil {
		return handleError(c, err)
	}
	userPath, err := scopedUserPath(c, "user_path", c.QueryParam("user_path"))
	if err != nil {
		return handleError(c, err)
	}

	interval := requestlog.StatsIntervalDay
	if dateRange.EndDate.Sub(dateRange.StartDate) < requestLogStatsHourlyRangeDays*24*time.Hour {
		interval = requestlog.StatsIntervalHour
	}

	if h.requestLogReader == nil {
		return c.JSON(http.StatusOK, requestlog.EmptyRequestStats(interval))
	}

	_, location := dashboardTimeZone(c)
	params := requestlog.RequestStatsParams{
		StartDate: dateRange.StartDate,
		EndDate:   dateRange.EndDate,
		UserPath:  userPath,
		Interval:  interval,
		Location:  location,
		Now:       timeNow(),
	}

	stats, err := h.requestLogReader.GetRequestStats(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if stats == nil {
		stats = requestlog.EmptyRequestStats(interval)
	}
	return c.JSON(http.StatusOK, stats)
}

// RequestLogDetail handles GET /admin/request-logs/detail.
//
// @Summary      Get request log entry detail
// @Description  Returns one request log entry enriched with usage summary when available.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        log_id  query     string  true  "Request log entry ID"
// @Success      200  {object}  requestLogEntryResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      500  {object}  core.GatewayError
// @Router       /admin/request-logs/detail [get]
func (h *Handler) RequestLogDetail(c *echo.Context) error {
	logID := strings.TrimSpace(c.QueryParam("log_id"))
	if logID == "" {
		return handleError(c, core.NewInvalidRequestError("log_id is required", nil))
	}
	if h.requestLogReader == nil {
		return handleError(c, featureUnavailableError("request log detail is unavailable"))
	}

	entry, err := h.requestLogReader.GetLogByID(c.Request().Context(), logID)
	if err != nil {
		return handleError(c, err)
	}
	// An entry outside the caller's scope is reported exactly like a
	// missing one so IDs from other tenants cannot be probed.
	if entry == nil || !requestScope(c).Allows(entry.UserPath) {
		return handleError(c, core.NewNotFoundError("request log not found: "+logID))
	}

	response, err := h.requestLogResponse(c.Request().Context(), &requestlog.LogListResult{
		Entries: []requestlog.LogEntry{*entry},
		Total:   1,
		Limit:   1,
	})
	if err != nil {
		return handleError(c, err)
	}
	if len(response.Entries) == 0 {
		return handleError(c, core.NewNotFoundError("request log not found: "+logID))
	}
	return c.JSON(http.StatusOK, response.Entries[0])
}

// RequestLogConversation handles GET /admin/request-logs/conversation
//
// @Summary      Get the interaction session containing a request log entry
// @Description  Thread entries carry the request/response bodies the
// @Description  transcript is built from; attempts, request revisions, and
// @Description  response headers are omitted; redacted request headers and
// @Description  per-request usage summaries are retained for continuation
// @Description  and prompt-cache visualization.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        log_id  query     string  true   "Anchor request log entry ID"
// @Param        limit   query     int     false  "Max entries in thread (default 40, max 200)"
// @Success      200  {object}  requestLogConversationResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Router       /admin/request-logs/conversation [get]
func (h *Handler) RequestLogConversation(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for missing/invalid params, regardless of whether
	// request logging is configured.
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

	if h.requestLogReader == nil {
		return c.JSON(http.StatusOK, requestLogConversationResponse{
			AnchorID: logID,
			Entries:  []requestLogEntryResponse{},
		})
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), conversationBuildTimeout)
	defer cancel()
	result, err := h.requestLogReader.GetConversation(ctx, logID, limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return handleError(c, core.NewProviderError("", http.StatusGatewayTimeout,
				"request log conversation lookup timed out", err))
		}
		return handleError(c, err)
	}
	if result == nil {
		result = &requestlog.ConversationResult{
			AnchorID: logID,
			Entries:  []requestlog.LogEntry{},
		}
	}
	if result.Entries == nil {
		result.Entries = []requestlog.LogEntry{}
	}
	result.Entries, err = scopedConversationEntries(c, logID, result.Entries)
	if err != nil {
		return handleError(c, err)
	}
	enriched, err := h.requestLogResponse(ctx, &requestlog.LogListResult{Entries: result.Entries})
	if err != nil {
		return handleError(c, err)
	}
	for i := range enriched.Entries {
		slimConversationEntry(&enriched.Entries[i].LogEntry)
	}

	return c.JSON(http.StatusOK, requestLogConversationResponse{
		AnchorID:  result.AnchorID,
		Entries:   enriched.Entries,
		Truncated: result.Truncated,
	})
}

// scopedConversationEntries drops thread entries outside the caller's scope.
// A scoped caller whose anchor entry lies outside its subtree gets the same
// not-found answer a missing entry produces. Global scopes pass through.
func scopedConversationEntries(c *echo.Context, anchorID string, entries []requestlog.LogEntry) ([]requestlog.LogEntry, error) {
	scope := requestScope(c)
	if scope.Global() {
		return entries, nil
	}
	kept := make([]requestlog.LogEntry, 0, len(entries))
	anchorVisible := false
	for _, entry := range entries {
		if !scope.Allows(entry.UserPath) {
			continue
		}
		if entry.ID == anchorID {
			anchorVisible = true
		}
		kept = append(kept, entry)
	}
	if !anchorVisible {
		return nil, core.NewNotFoundError("request log not found: " + anchorID)
	}
	return kept, nil
}
