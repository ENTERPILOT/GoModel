package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/budget"
	"gomodel/internal/core"
	"gomodel/internal/ratelimit"
	"gomodel/internal/usage"
)

// UsageSummarizer aggregates recorded usage entries for the self-service
// usage endpoint. Usage readers satisfy it; the endpoint deliberately needs
// only the summary slice of the full reader interface.
type UsageSummarizer interface {
	GetSummary(ctx context.Context, params usage.UsageQueryParams) (*usage.UsageSummary, error)
}

// budgetStatusReporter and rateLimitStatusReporter are optional upgrades of
// the enforcement interfaces already wired into the handler. The concrete
// budget and rate limit services implement them; enforcement-only fakes keep
// working and simply yield no status.
type budgetStatusReporter interface {
	StatusesForPath(ctx context.Context, userPath string, now time.Time) ([]budget.CheckResult, error)
}

type rateLimitStatusReporter interface {
	StatusesForUserPath(userPath string, now time.Time) []ratelimit.Status
}

// usageStatusResponse is the self-service view of one user path: recorded
// usage over a date window plus every budget and rate limit rule gating it.
type usageStatusResponse struct {
	UserPath   string                 `json:"user_path"`
	ServerTime time.Time              `json:"server_time"`
	Usage      *usageStatusSummary    `json:"usage"`
	Budgets    []usageStatusBudget    `json:"budgets"`
	RateLimits []usageStatusRateLimit `json:"rate_limits"`
}

type usageStatusSummary struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	usage.UsageSummary
}

type usageStatusBudget struct {
	UserPath      string  `json:"user_path"`
	PeriodSeconds int64   `json:"period_seconds"`
	PeriodLabel   string  `json:"period_label"`
	Amount        float64 `json:"amount"`
	Spent         float64 `json:"spent"`
	Remaining     float64 `json:"remaining"`
	// UsageRatio is spent/amount, deliberately unclamped: values above 1
	// mean the budget is blown through.
	UsageRatio      float64   `json:"usage_ratio"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	ResetsInSeconds int64     `json:"resets_in_seconds"`
	Exceeded        bool      `json:"exceeded"`
}

type usageStatusRateLimit struct {
	UserPath          string `json:"user_path"`
	PeriodSeconds     int64  `json:"period_seconds"`
	PeriodLabel       string `json:"period_label"`
	MaxRequests       *int64 `json:"max_requests,omitempty"`
	MaxTokens         *int64 `json:"max_tokens,omitempty"`
	RequestsUsed      int64  `json:"requests_used"`
	RequestsRemaining *int64 `json:"requests_remaining,omitempty"`
	// The usage ratios are used/limit per dimension, present only when that
	// limit exists and unclamped (token windows can overshoot past 1).
	RequestsUsageRatio *float64   `json:"requests_usage_ratio,omitempty"`
	TokensUsed         int64      `json:"tokens_used"`
	TokensRemaining    *int64     `json:"tokens_remaining,omitempty"`
	TokensUsageRatio   *float64   `json:"tokens_usage_ratio,omitempty"`
	InFlight           int64      `json:"in_flight"`
	WindowStart        *time.Time `json:"window_start,omitempty"`
	WindowEnd          *time.Time `json:"window_end,omitempty"`
	ResetsInSeconds    *int64     `json:"resets_in_seconds,omitempty"`
	Exhausted          bool       `json:"exhausted"`
}

// UsageStatus handles GET /v1/usage.
//
// @Summary      Self-service usage, budget, and rate limit status
// @Description  Returns recorded usage, budget statuses, and rate limit statuses for the caller's effective user path (the path bound to the managed API key, or the user-path header for master-key callers).
// @Tags         usage
// @Produce      json
// @Security     BearerAuth
// @Param        start_date  query  string  false  "Inclusive window start (YYYY-MM-DD, UTC); defaults to 29 days before end_date"
// @Param        end_date    query  string  false  "Inclusive window end (YYYY-MM-DD, UTC); defaults to today; the whole range may span at most 365 days"
// @Param        days        query  int     false  "Window length ending today when no explicit dates are given (default 30, max 365)"
// @Success      200  {object}  usageStatusResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      503  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/usage [get]
func (h *Handler) UsageStatus(c *echo.Context) error {
	ctx := c.Request().Context()
	now := time.Now().UTC()

	userPath, err := h.usageStatusUserPath(c)
	if err != nil {
		return handleError(c, err)
	}

	params, err := usageStatusWindow(c, now)
	if err != nil {
		return handleError(c, err)
	}
	params.UserPath = userPath

	response := usageStatusResponse{
		UserPath:   userPath,
		ServerTime: now,
		Budgets:    []usageStatusBudget{},
		RateLimits: []usageStatusRateLimit{},
	}

	if h.usageSummarizer != nil {
		summary, err := h.usageSummarizer.GetSummary(ctx, params)
		if err != nil {
			return handleError(c, core.NewProviderError("usage", http.StatusServiceUnavailable, "failed to read usage data", err).WithCode("usage_status_failed"))
		}
		if summary != nil {
			response.Usage = &usageStatusSummary{
				StartDate:    params.StartDate.Format("2006-01-02"),
				EndDate:      params.EndDate.Format("2006-01-02"),
				UsageSummary: *summary,
			}
		}
	}

	if reporter, ok := h.budgetChecker.(budgetStatusReporter); ok {
		results, err := reporter.StatusesForPath(ctx, userPath, now)
		if err != nil && !errors.Is(err, budget.ErrUnavailable) {
			return handleError(c, core.NewProviderError("budget", http.StatusServiceUnavailable, "failed to read budget status", err).WithCode("usage_status_failed"))
		}
		response.Budgets = usageStatusBudgets(results, now)
	}

	if reporter, ok := h.rateLimiter.(rateLimitStatusReporter); ok {
		response.RateLimits = usageStatusRateLimits(reporter.StatusesForUserPath(userPath, now), now)
	}

	return c.JSON(http.StatusOK, response)
}

// usageStatusUserPath resolves the caller's effective user path. Managed keys
// bind it via context; master-key and unsafe-mode callers may scope the
// request with the configured user-path header. /v1/usage is not an
// ingress-managed route, so the header is read here instead of from a request
// snapshot.
func (h *Handler) usageStatusUserPath(c *echo.Context) (string, error) {
	if userPath := core.UserPathFromContext(c.Request().Context()); userPath != "" {
		return userPath, nil
	}
	headerName := h.userPathHeaderName
	if headerName == "" {
		headerName = core.UserPathHeader
	}
	userPath, err := core.NormalizeUserPath(c.Request().Header.Get(headerName))
	if err != nil {
		return "", core.NewInvalidRequestError("invalid "+headerName+" header", err)
	}
	if userPath == "" {
		return "/", nil
	}
	return userPath, nil
}

// usageStatusWindow resolves the summary date window from the same query
// params as the dashboard usage endpoints, always in UTC.
func usageStatusWindow(c *echo.Context, now time.Time) (usage.UsageQueryParams, error) {
	params := usage.UsageQueryParams{TimeZone: "UTC"}
	days := usage.DefaultDateRangeDays
	if raw := c.QueryParam("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return params, core.NewInvalidRequestError("invalid days parameter, expected a positive integer", err)
		}
		days = usage.NormalizeDateRangeDays(parsed)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start, end, err := usage.BuildDateRange(strings.TrimSpace(c.QueryParam("start_date")), strings.TrimSpace(c.QueryParam("end_date")), days, time.UTC, today)
	if err != nil {
		return params, err
	}
	params.StartDate = start
	params.EndDate = end
	return params, nil
}

func usageStatusBudgets(results []budget.CheckResult, now time.Time) []usageStatusBudget {
	statuses := make([]usageStatusBudget, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, usageStatusBudget{
			UserPath:        result.Budget.UserPath,
			PeriodSeconds:   result.Budget.PeriodSeconds,
			PeriodLabel:     budget.PeriodLabel(result.Budget.PeriodSeconds),
			Amount:          result.Budget.Amount,
			Spent:           result.Spent,
			Remaining:       result.Remaining,
			UsageRatio:      result.UsageRatio(),
			PeriodStart:     result.PeriodStart,
			PeriodEnd:       result.PeriodEnd,
			ResetsInSeconds: secondsUntil(result.PeriodEnd, now),
			// Mirrors enforcement: budgets without any usage never block.
			Exceeded: result.HasUsage && result.Spent >= result.Budget.Amount,
		})
	}
	return statuses
}

func usageStatusRateLimits(statuses []ratelimit.Status, now time.Time) []usageStatusRateLimit {
	limits := make([]usageStatusRateLimit, 0, len(statuses))
	for _, status := range statuses {
		item := usageStatusRateLimit{
			UserPath:          status.Rule.Subject,
			PeriodSeconds:     status.Rule.PeriodSeconds,
			PeriodLabel:       ratelimit.PeriodLabel(status.Rule.PeriodSeconds),
			MaxRequests:       status.Rule.MaxRequests,
			MaxTokens:         status.Rule.MaxTokens,
			RequestsUsed:      status.RequestsUsed,
			RequestsRemaining: status.RequestsRemaining,
			TokensUsed:        status.TokensUsed,
			TokensRemaining:   status.TokensRemaining,
			InFlight:          status.InFlight,
			// Exhausted mirrors admission: any fully used dimension rejects
			// (or queues behind) the next request.
			Exhausted: (status.RequestsRemaining != nil && *status.RequestsRemaining == 0) ||
				(status.TokensRemaining != nil && *status.TokensRemaining == 0),
		}
		requestsUsed := status.RequestsUsed
		if status.Rule.PeriodSeconds == ratelimit.PeriodConcurrent {
			requestsUsed = status.InFlight
		}
		item.RequestsUsageRatio = limitUsageRatio(requestsUsed, status.Rule.MaxRequests)
		item.TokensUsageRatio = limitUsageRatio(status.TokensUsed, status.Rule.MaxTokens)
		if !status.WindowStart.IsZero() {
			start, end := status.WindowStart, status.WindowEnd
			item.WindowStart = &start
			item.WindowEnd = &end
			resets := secondsUntil(end, now)
			item.ResetsInSeconds = &resets
		}
		limits = append(limits, item)
	}
	return limits
}

// limitUsageRatio returns used/limit for one limit dimension, nil when the
// dimension has no limit. Deliberately unclamped, matching budget usage
// ratios: token windows can overshoot past 1.
func limitUsageRatio(used int64, limit *int64) *float64 {
	if limit == nil || *limit <= 0 {
		return nil
	}
	ratio := float64(used) / float64(*limit)
	return &ratio
}

// secondsUntil returns the whole seconds from now until t, never negative.
func secondsUntil(t time.Time, now time.Time) int64 {
	seconds := int64(math.Ceil(t.Sub(now).Seconds()))
	if seconds < 0 {
		return 0
	}
	return seconds
}
