package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

// ListRateLimits handles GET /admin/rate-limits.
//
// @Summary      List rate limit rules with live counter status
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  rateLimitListResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/rate-limits [get]
func (h *Handler) ListRateLimits(c *echo.Context) error {
	if h.rateLimits == nil {
		return handleError(c, featureUnavailableError("rate limits feature is unavailable"))
	}
	now := time.Now().UTC()
	return c.JSON(http.StatusOK, rateLimitListResponse{
		RateLimits: rateLimitStatusResponses(h.rateLimits.Statuses(now)),
		ServerTime: now,
	})
}

// UpsertRateLimit handles PUT /admin/rate-limits.
// @Summary      Create or update one rate limit rule
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        rule  body      upsertRateLimitRequest  true  "Rate limit key and limits"
// @Success      200   {object}  rateLimitListResponse
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/rate-limits [put]
func (h *Handler) UpsertRateLimit(c *echo.Context) error {
	if h.rateLimits == nil {
		return handleError(c, featureUnavailableError("rate limits feature is unavailable"))
	}
	var req upsertRateLimitRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	scope, subject, periodSeconds, err := rateLimitRequestKey(req.Scope, req.Subject, req.UserPath, req.LimitKey)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	item, err := ratelimit.NormalizeRule(ratelimit.Rule{
		Scope:         scope,
		Subject:       subject,
		PeriodSeconds: periodSeconds,
		MaxRequests:   req.MaxRequests,
		MaxTokens:     req.MaxTokens,
		Source:        ratelimit.SourceManual,
	})
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.rateLimits.UpsertRules(c.Request().Context(), []ratelimit.Rule{item}); err != nil {
		return handleError(c, rateLimitServiceError("failed to save rate limit rule", err))
	}
	return h.ListRateLimits(c)
}

// DeleteRateLimit handles DELETE /admin/rate-limits.
// @Summary      Delete one rate limit rule
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        rule  body      deleteRateLimitRequest  true  "Rate limit key"
// @Success      200   {object}  rateLimitListResponse
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/rate-limits [delete]
//
//nolint:dupl // structurally similar to DeleteBudget but operates on different types and services.
func (h *Handler) DeleteRateLimit(c *echo.Context) error {
	if h.rateLimits == nil {
		return handleError(c, featureUnavailableError("rate limits feature is unavailable"))
	}
	var req deleteRateLimitRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	scope, subject, periodSeconds, err := rateLimitRequestKey(req.Scope, req.Subject, req.UserPath, req.LimitKey)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.rateLimits.DeleteRule(c.Request().Context(), scope, subject, periodSeconds); err != nil {
		return handleError(c, rateLimitServiceError("failed to delete rate limit rule", err))
	}
	return h.ListRateLimits(c)
}

// ResetRateLimit handles POST /admin/rate-limits/reset-one.
// @Summary      Reset the live counters of one rate limit rule
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        rule  body      resetRateLimitRequest  true  "Rate limit key"
// @Success      200   {object}  rateLimitListResponse
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/rate-limits/reset-one [post]
func (h *Handler) ResetRateLimit(c *echo.Context) error {
	if h.rateLimits == nil {
		return handleError(c, featureUnavailableError("rate limits feature is unavailable"))
	}
	var req resetRateLimitRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	scope, subject, periodSeconds, err := rateLimitRequestKey(req.Scope, req.Subject, req.UserPath, &rateLimitKeyRequest{Period: req.Period, PeriodSeconds: req.PeriodSeconds})
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.rateLimits.ResetRule(scope, subject, periodSeconds); err != nil {
		return handleError(c, rateLimitServiceError("failed to reset rate limit rule", err))
	}
	return h.ListRateLimits(c)
}

// ResetRateLimits handles POST /admin/rate-limits/reset.
// @Summary      Reset the live counters of all rate limit rules
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        confirmation  body      resetRateLimitsRequest  true  "Reset confirmation"
// @Success      200           {object}  resetRateLimitsResponse
// @Failure      400           {object}  core.GatewayError
// @Failure      401           {object}  core.GatewayError
// @Failure      503           {object}  core.GatewayError
// @Router       /admin/rate-limits/reset [post]
func (h *Handler) ResetRateLimits(c *echo.Context) error {
	if h.rateLimits == nil {
		return handleError(c, featureUnavailableError("rate limits feature is unavailable"))
	}
	var req resetRateLimitsRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(strings.ToLower(req.Confirmation)) != "reset" {
		return handleError(c, core.NewInvalidRequestError("confirmation must be reset", nil))
	}
	if err := h.rateLimits.ResetAll(); err != nil {
		return handleError(c, rateLimitServiceError("failed to reset rate limits", err))
	}
	return c.JSON(http.StatusOK, resetRateLimitsResponse{Status: "ok"})
}

type rateLimitListResponse struct {
	RateLimits []rateLimitStatusResponse `json:"rate_limits"`
	ServerTime time.Time                 `json:"server_time"`
}

type rateLimitStatusResponse struct {
	Scope             string     `json:"scope"`
	Subject           string     `json:"subject"`
	UserPath          string     `json:"user_path,omitempty"`
	PeriodSeconds     int64      `json:"period_seconds"`
	PeriodLabel       string     `json:"period_label"`
	MaxRequests       *int64     `json:"max_requests,omitempty"`
	MaxTokens         *int64     `json:"max_tokens,omitempty"`
	Source            string     `json:"source,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	WindowStart       *time.Time `json:"window_start,omitempty"`
	WindowEnd         *time.Time `json:"window_end,omitempty"`
	RequestsUsed      int64      `json:"requests_used"`
	RequestsRemaining *int64     `json:"requests_remaining,omitempty"`
	TokensUsed        int64      `json:"tokens_used"`
	TokensRemaining   *int64     `json:"tokens_remaining,omitempty"`
	InFlight          int64      `json:"in_flight"`
}

type upsertRateLimitRequest struct {
	Scope       string               `json:"scope"`
	Subject     string               `json:"subject"`
	UserPath    string               `json:"user_path"`
	LimitKey    *rateLimitKeyRequest `json:"limit_key"`
	MaxRequests *int64               `json:"max_requests"`
	MaxTokens   *int64               `json:"max_tokens"`
}

type deleteRateLimitRequest struct {
	Scope    string               `json:"scope"`
	Subject  string               `json:"subject"`
	UserPath string               `json:"user_path"`
	LimitKey *rateLimitKeyRequest `json:"limit_key"`
}

type rateLimitKeyRequest struct {
	Period        string `json:"period,omitempty"`
	PeriodSeconds *int64 `json:"period_seconds,omitempty"`
}

type resetRateLimitRequest struct {
	Scope         string `json:"scope"`
	Subject       string `json:"subject"`
	UserPath      string `json:"user_path"`
	Period        string `json:"period,omitempty"`
	PeriodSeconds *int64 `json:"period_seconds,omitempty"`
}

type resetRateLimitsRequest struct {
	Confirmation string `json:"confirmation"`
}

type resetRateLimitsResponse struct {
	Status string `json:"status"`
}

func rateLimitStatusResponses(statuses []ratelimit.Status) []rateLimitStatusResponse {
	responses := make([]rateLimitStatusResponse, 0, len(statuses))
	for _, status := range statuses {
		rule := status.Rule
		item := rateLimitStatusResponse{
			Scope:             string(rule.Scope),
			Subject:           rule.Subject,
			PeriodSeconds:     rule.PeriodSeconds,
			PeriodLabel:       ratelimit.PeriodLabel(rule.PeriodSeconds),
			MaxRequests:       rule.MaxRequests,
			MaxTokens:         rule.MaxTokens,
			Source:            rule.Source,
			CreatedAt:         rule.CreatedAt,
			UpdatedAt:         rule.UpdatedAt,
			RequestsUsed:      status.RequestsUsed,
			RequestsRemaining: status.RequestsRemaining,
			TokensUsed:        status.TokensUsed,
			TokensRemaining:   status.TokensRemaining,
			InFlight:          status.InFlight,
		}
		// Convenience duplicate: user-path rules keep the natural spelling.
		if rule.Scope == ratelimit.ScopeUserPath {
			item.UserPath = rule.Subject
		}
		if !status.WindowStart.IsZero() {
			start := status.WindowStart
			end := status.WindowEnd
			item.WindowStart = &start
			item.WindowEnd = &end
		}
		responses = append(responses, item)
	}
	return responses
}

// rateLimitRequestKey resolves the rule identity from a request. The subject
// may arrive as `subject` (any scope) or as `user_path` (the natural spelling
// for user-path rules); scope defaults to user_path.
func rateLimitRequestKey(rawScope, rawSubject, rawUserPath string, key *rateLimitKeyRequest) (ratelimit.RuleScope, string, int64, error) {
	scope, err := ratelimit.NormalizeScope(rawScope)
	if err != nil {
		return "", "", 0, err
	}
	rawSubject = strings.TrimSpace(rawSubject)
	if rawSubject == "" {
		if scope != ratelimit.ScopeUserPath && strings.TrimSpace(rawUserPath) != "" {
			return "", "", 0, errors.New("subject is required for provider and model rules; user_path only names user-path rules")
		}
		rawSubject = rawUserPath
	} else if scope != ratelimit.ScopeUserPath && strings.TrimSpace(rawUserPath) != "" {
		// Silently dropping the conflicting field would mask rule-authoring
		// mistakes.
		return "", "", 0, errors.New("user_path must not be set alongside subject for provider and model rules")
	}
	subject, err := ratelimit.NormalizeSubject(scope, rawSubject)
	if err != nil {
		return "", "", 0, err
	}
	if key == nil {
		return "", "", 0, errors.New("limit_key is required")
	}
	periodSeconds, err := rateLimitRequestPeriodSeconds(key.Period, key.PeriodSeconds)
	if err != nil {
		return "", "", 0, err
	}
	return scope, subject, periodSeconds, nil
}

func rateLimitRequestPeriodSeconds(period string, periodSeconds *int64) (int64, error) {
	hasPeriod := strings.TrimSpace(period) != ""
	if hasPeriod && periodSeconds != nil {
		return 0, errors.New("set either period or period_seconds, not both")
	}
	if periodSeconds != nil {
		if *periodSeconds < 0 {
			return 0, errors.New("period_seconds must be 0 (concurrent) or greater")
		}
		return *periodSeconds, nil
	}
	if !hasPeriod {
		return 0, errors.New("period or period_seconds is required")
	}
	if parsed, ok := ratelimit.PeriodSecondsFromName(period); ok {
		return parsed, nil
	}
	if parsed, err := strconv.ParseInt(strings.TrimSpace(period), 10, 64); err == nil {
		if parsed < 0 {
			return 0, errors.New("period_seconds must be 0 (concurrent) or greater")
		}
		return parsed, nil
	}
	return 0, errors.New("period must be one of minute, hour, day, concurrent or period_seconds must be set")
}
