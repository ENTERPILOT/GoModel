package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// maxUsageLogLimit caps the page size accepted by the usage log endpoint and
// matches the value documented in the @Param limit annotation below.
const maxUsageLogLimit = 200

// defaultUsageLogLimit is the effective page size when the caller omits limit.
// It mirrors the reader's pagination default so the disabled-reader fast path
// reports the same limit an enabled reader would.
const defaultUsageLogLimit = 50

// UsageSummary handles GET /admin/usage/summary
//
// @Summary      Get usage summary
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {object}  usage.UsageSummary
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/usage/summary [get]
func (h *Handler) UsageSummary(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of wiring.
	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if h.usageReader == nil {
		return c.JSON(http.StatusOK, usage.UsageSummary{})
	}

	summary, err := h.usageReader.GetSummary(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if summary == nil {
		summary = &usage.UsageSummary{}
	}

	return c.JSON(http.StatusOK, summary)
}

func usageSliceResponse[T any](
	c *echo.Context,
	reader usage.UsageReader,
	fetch func(context.Context, usage.UsageQueryParams) ([]T, error),
) error {
	// Validate before the disabled-reader fast path so malformed query
	// params produce a 400 even when usage tracking is disabled.
	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	if reader == nil {
		return c.JSON(http.StatusOK, []T{})
	}

	values, err := fetch(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if values == nil {
		values = []T{}
	}
	return c.JSON(http.StatusOK, values)
}

// DailyUsage handles GET /admin/usage/daily
//
// @Summary      Get usage breakdown by period
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        interval    query     string  false  "Grouping interval: daily, weekly, monthly, yearly (default daily)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.DailyUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/usage/daily [get]
func (h *Handler) DailyUsage(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.DailyUsage, error) {
		return h.usageReader.GetDailyUsage(ctx, params)
	})
}

// UsageByModel handles GET /admin/usage/models
//
// @Summary      Get usage breakdown by model
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.ModelUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/usage/models [get]
func (h *Handler) UsageByModel(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.ModelUsage, error) {
		return h.usageReader.GetUsageByModel(ctx, params)
	})
}

// UsageByUserPath handles GET /admin/usage/user-paths
//
// @Summary      Get usage breakdown by user path
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.UserPathUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/usage/user-paths [get]
func (h *Handler) UsageByUserPath(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.UserPathUsage, error) {
		return h.usageReader.GetUsageByUserPath(ctx, params)
	})
}

// UsageLog handles GET /admin/usage/log
//
// @Summary      Get paginated usage log entries
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        model       query     string  false  "Filter by model name"
// @Param        provider    query     string  false  "Filter by provider name or provider type"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Param        search      query     string  false  "Search across model, provider, request_id, provider_id"
// @Param        limit       query     int     false  "Page size (default 50, max 200)"
// @Param        offset      query     int     false  "Offset for pagination"
// @Success      200  {object}  usage.UsageLogResult
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/usage/log [get]
func (h *Handler) UsageLog(c *echo.Context) error {
	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of wiring.
	baseParams, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	params := usage.UsageLogParams{
		UsageQueryParams: baseParams,
		Model:            c.QueryParam("model"),
		Provider:         c.QueryParam("provider"),
		Search:           c.QueryParam("search"),
	}

	if l := c.QueryParam("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			return handleError(c, core.NewInvalidRequestError("invalid limit, expected positive integer", nil))
		}
		if parsed > maxUsageLogLimit {
			return handleError(c, core.NewInvalidRequestError("invalid limit parameter: limit must be between 1 and 200", nil))
		}
		params.Limit = parsed
	}
	if o := c.QueryParam("offset"); o != "" {
		parsed, err := strconv.Atoi(o)
		if err != nil || parsed < 0 {
			return handleError(c, core.NewInvalidRequestError("invalid offset, expected non-negative integer", nil))
		}
		params.Offset = parsed
	}

	if h.usageReader == nil {
		// Echo the effective pagination so the response matches the enabled-reader
		// contract. Returning limit:0 here would make the client send limit=0 on
		// its next request, which fails validation above with a 400.
		limit := params.Limit
		if limit <= 0 {
			limit = defaultUsageLogLimit
		}
		return c.JSON(http.StatusOK, usage.UsageLogResult{
			Entries: []usage.UsageLogEntry{},
			Limit:   limit,
			Offset:  params.Offset,
		})
	}

	result, err := h.usageReader.GetUsageLog(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &usage.UsageLogResult{Entries: []usage.UsageLogEntry{}}
	}
	if result.Entries == nil {
		result.Entries = []usage.UsageLogEntry{}
	}
	for i := range result.Entries {
		usage.EnrichUsageLogEntry(&result.Entries[i])
	}

	return c.JSON(http.StatusOK, result)
}

// RecalculateUsagePricing handles POST /admin/usage/recalculate-pricing.
//
// @Summary      Recalculate stored usage costs from current model pricing metadata
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      recalculatePricingRequest       true  "Recalculation filters and confirmation"
// @Success      200      {object}  usage.RecalculatePricingResult
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      500      {object}  core.GatewayError
// @Failure      503      {object}  core.GatewayError
// @Router       /admin/usage/recalculate-pricing [post]
func (h *Handler) RecalculateUsagePricing(c *echo.Context) error {
	if h.usageRecalculator == nil {
		return handleError(c, featureUnavailableError("usage pricing recalculation is unavailable"))
	}
	if h.pricingResolver == nil {
		return handleError(c, featureUnavailableError("model pricing metadata is unavailable"))
	}

	var req recalculatePricingRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(strings.ToLower(req.confirmationValue())) != "recalculate" {
		return handleError(c, core.NewInvalidRequestError("confirmation must be recalculate", nil))
	}

	params, err := h.recalculatePricingParams(c, req)
	if err != nil {
		return handleError(c, err)
	}

	h.pricingMu.Lock()
	defer h.pricingMu.Unlock()

	result, err := h.usageRecalculator.RecalculatePricing(c.Request().Context(), params, h.pricingResolver)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return handleError(c, err)
		}
		if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
			return handleError(c, gatewayErr)
		}
		return handleError(c, core.NewProviderError("usage", http.StatusInternalServerError, "failed to recalculate usage pricing", err))
	}
	return c.JSON(http.StatusOK, result)
}

// CacheOverview handles GET /admin/cache/overview
//
// @Summary      Get cached-only usage overview
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        interval    query     string  false  "Grouping interval: daily, weekly, monthly, yearly (default daily)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (cache overview always uses cached mode)"
// @Success      200  {object}  usage.CacheOverview
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/cache/overview [get]
func (h *Handler) CacheOverview(c *echo.Context) error {
	// Feature-gate check stays first: this endpoint is conceptually unavailable
	// when cache analytics is off, and the response shape (503) communicates
	// that to the dashboard.
	if strings.TrimSpace(h.runtimeConfig.CacheEnabled) != "on" {
		return handleError(c, featureUnavailableError("cache analytics is unavailable"))
	}

	// Validate request shape before the disabled-reader fast path so callers
	// always get a 400 for malformed inputs, regardless of wiring.
	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}
	params.CacheMode = usage.CacheModeCached

	if h.usageReader == nil {
		return c.JSON(http.StatusOK, usage.CacheOverview{
			Daily: []usage.CacheOverviewDaily{},
		})
	}

	overview, err := h.usageReader.GetCacheOverview(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if overview == nil {
		overview = &usage.CacheOverview{}
	}
	if overview.Daily == nil {
		overview.Daily = []usage.CacheOverviewDaily{}
	}

	return c.JSON(http.StatusOK, overview)
}

type recalculatePricingRequest struct {
	Days         int    `json:"days,omitempty"`
	StartDate    string `json:"start_date,omitempty"`
	EndDate      string `json:"end_date,omitempty"`
	UserPath     string `json:"user_path,omitempty"`
	Selector     string `json:"selector,omitempty"`
	Confirmation string `json:"confirmation"`
	Confirm      string `json:"confirm,omitempty"`
}

func (r recalculatePricingRequest) confirmationValue() string {
	if strings.TrimSpace(r.Confirmation) != "" {
		return r.Confirmation
	}
	return r.Confirm
}

func (h *Handler) recalculatePricingParams(c *echo.Context, req recalculatePricingRequest) (usage.RecalculatePricingParams, error) {
	baseParams, err := recalculatePricingDateParams(c, req)
	if err != nil {
		return usage.RecalculatePricingParams{}, err
	}

	userPath, err := normalizeUserPathQueryParam("user_path", req.UserPath)
	if err != nil {
		return usage.RecalculatePricingParams{}, err
	}
	baseParams.UserPath = userPath
	baseParams.CacheMode = usage.CacheModeAll

	provider, model, err := h.recalculatePricingSelector(req.Selector)
	if err != nil {
		return usage.RecalculatePricingParams{}, err
	}

	return usage.RecalculatePricingParams{
		UsageQueryParams: baseParams,
		Provider:         provider,
		Model:            model,
	}, nil
}

func recalculatePricingDateParams(c *echo.Context, req recalculatePricingRequest) (usage.UsageQueryParams, error) {
	var params usage.UsageQueryParams

	timeZone, location := dashboardTimeZone(c)
	params.TimeZone = timeZone

	now := timeNow().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)

	start, end, err := buildDateRange(strings.TrimSpace(req.StartDate), strings.TrimSpace(req.EndDate), normalizeDateRangeDays(req.Days), location, today)
	if err != nil {
		return params, err
	}
	params.StartDate = start
	params.EndDate = end
	return params, nil
}

func (h *Handler) recalculatePricingSelector(raw string) (provider, model string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}

	if h.virtualModels != nil {
		selector, changed, err := h.virtualModels.ResolveModel(core.NewRequestedModelSelector(raw, ""))
		if err != nil {
			return "", "", core.NewInvalidRequestError("invalid selector: "+err.Error(), err)
		}
		if changed {
			return selector.Provider, selector.Model, nil
		}
	}

	selector, err := core.ParseModelSelector(raw, "")
	if err != nil {
		return "", "", core.NewInvalidRequestError("invalid selector: "+err.Error(), err)
	}
	if selector.Provider == "" {
		return "", "", core.NewInvalidRequestError("invalid selector: provider/model or alias is required", nil)
	}
	return selector.Provider, selector.Model, nil
}
