package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
)

// ListBudgets handles GET /admin/budgets.
//
// @Summary      List budgets with current status
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  budgetListResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/budgets [get]
func (h *Handler) ListBudgets(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	now := time.Now().UTC()
	statuses, err := h.budgets.Statuses(c.Request().Context(), now)
	if err != nil {
		return handleError(c, budgetServiceError("failed to list budgets", err))
	}
	return c.JSON(http.StatusOK, budgetListResponse{
		Budgets:    budgetStatusResponses(statuses, now),
		ServerTime: now,
	})
}

// UpsertBudget handles PUT /admin/budgets.
// @Summary      Create or update one budget
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        budget     body      upsertBudgetRequest  true  "Budget key and amount"
// @Success      200        {object}  budgetListResponse
// @Failure      400        {object}  core.GatewayError
// @Failure      401        {object}  core.GatewayError
// @Failure      503        {object}  core.GatewayError
// @Router       /admin/budgets [put]
func (h *Handler) UpsertBudget(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	var req upsertBudgetRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	userPath, periodSeconds, err := budgetRequestKey(req.UserPath, req.BudgetKey)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	item, err := budget.NormalizeBudget(budget.Budget{
		UserPath:      userPath,
		PeriodSeconds: periodSeconds,
		Amount:        req.Amount,
		Source:        budget.SourceManual,
	})
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.budgets.UpsertBudgets(c.Request().Context(), []budget.Budget{item}); err != nil {
		return handleError(c, budgetServiceError("failed to save budget", err))
	}
	return h.ListBudgets(c)
}

// DeleteBudget handles DELETE /admin/budgets.
// @Summary      Delete one budget
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        budget     body      deleteBudgetRequest  true  "Budget key"
// @Success      200        {object}  budgetListResponse
// @Failure      400        {object}  core.GatewayError
// @Failure      401        {object}  core.GatewayError
// @Failure      503        {object}  core.GatewayError
// @Router       /admin/budgets [delete]
//
//nolint:dupl // structurally similar to DeleteRateLimit but operates on different types and services.
func (h *Handler) DeleteBudget(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	var req deleteBudgetRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	userPath, periodSeconds, err := budgetRequestKey(req.UserPath, req.BudgetKey)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.budgets.DeleteBudget(c.Request().Context(), userPath, periodSeconds); err != nil {
		return handleError(c, budgetServiceError("failed to delete budget", err))
	}
	return h.ListBudgets(c)
}

// BudgetSettings handles GET /admin/budgets/settings.
// @Summary      Get budget reset settings
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  budget.Settings
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/budgets/settings [get]
func (h *Handler) BudgetSettings(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	return c.JSON(http.StatusOK, h.budgets.Settings())
}

// UpdateBudgetSettings handles PUT /admin/budgets/settings.
// @Summary      Update budget reset settings
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        settings  body      updateBudgetSettingsRequest  true  "Budget reset settings"
// @Success      200       {object}  budget.Settings
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/budgets/settings [put]
func (h *Handler) UpdateBudgetSettings(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	var req updateBudgetSettingsRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	settings := req.apply(h.budgets.Settings())
	if err := budget.ValidateSettings(settings); err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	saved, err := h.budgets.SaveSettings(c.Request().Context(), settings)
	if err != nil {
		return handleError(c, budgetServiceError("failed to save budget settings", err))
	}
	return c.JSON(http.StatusOK, saved)
}

// ResetBudget handles POST /admin/budgets/reset-one.
// @Summary      Reset one budget period
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        budget  body      resetBudgetRequest  true  "Budget key"
// @Success      200     {object}  budgetListResponse
// @Failure      400     {object}  core.GatewayError
// @Failure      401     {object}  core.GatewayError
// @Failure      503     {object}  core.GatewayError
// @Router       /admin/budgets/reset-one [post]
func (h *Handler) ResetBudget(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	var req resetBudgetRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	periodSeconds, err := budgetRequestPeriodSeconds(req.Period, req.PeriodSeconds)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	userPath, err := budget.NormalizeUserPath(req.UserPath)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError(err.Error(), err))
	}
	if err := h.budgets.ResetBudget(c.Request().Context(), userPath, periodSeconds, time.Now().UTC()); err != nil {
		return handleError(c, budgetServiceError("failed to reset budget", err))
	}
	return h.ListBudgets(c)
}

// ResetBudgets handles POST /admin/budgets/reset.
// @Summary      Reset all budget periods
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        confirmation  body      resetBudgetsRequest  true  "Reset confirmation"
// @Success      200           {object}  resetBudgetsResponse
// @Failure      400           {object}  core.GatewayError
// @Failure      401           {object}  core.GatewayError
// @Failure      503           {object}  core.GatewayError
// @Router       /admin/budgets/reset [post]
func (h *Handler) ResetBudgets(c *echo.Context) error {
	if h.budgets == nil {
		return handleError(c, featureUnavailableError("budgets feature is unavailable"))
	}
	var req resetBudgetsRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(strings.ToLower(req.confirmationValue())) != "reset" {
		return handleError(c, core.NewInvalidRequestError("confirmation must be reset", nil))
	}
	if err := h.budgets.ResetAll(c.Request().Context(), time.Now().UTC()); err != nil {
		return handleError(c, budgetServiceError("failed to reset budgets", err))
	}
	return c.JSON(http.StatusOK, resetBudgetsResponse{Status: "ok"})
}

type budgetListResponse struct {
	Budgets    []budgetStatusResponse `json:"budgets"`
	ServerTime time.Time              `json:"server_time"`
}

type budgetStatusResponse struct {
	UserPath      string     `json:"user_path"`
	PeriodSeconds int64      `json:"period_seconds"`
	PeriodLabel   string     `json:"period_label"`
	Amount        float64    `json:"amount"`
	Source        string     `json:"source,omitempty"`
	LastResetAt   *time.Time `json:"last_reset_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	PeriodStart   time.Time  `json:"period_start"`
	PeriodEnd     time.Time  `json:"period_end"`
	Spent         float64    `json:"spent"`
	HasUsage      bool       `json:"has_usage"`
	Remaining     float64    `json:"remaining"`
	UsageRatio    float64    `json:"usage_ratio"`
	PeriodRatio   float64    `json:"period_ratio"`
}

type upsertBudgetRequest struct {
	UserPath  string            `json:"user_path"`
	BudgetKey *budgetKeyRequest `json:"budget_key"`
	Amount    float64           `json:"amount"`
}

type deleteBudgetRequest struct {
	UserPath  string            `json:"user_path"`
	BudgetKey *budgetKeyRequest `json:"budget_key"`
}

type budgetKeyRequest struct {
	Period        string `json:"period,omitempty"`
	PeriodSeconds int64  `json:"period_seconds,omitempty"`
}

type resetBudgetRequest struct {
	UserPath      string `json:"user_path"`
	Period        string `json:"period,omitempty"`
	PeriodSeconds int64  `json:"period_seconds,omitempty"`
}

type updateBudgetSettingsRequest struct {
	DailyResetHour     *int `json:"daily_reset_hour"`
	DailyResetMinute   *int `json:"daily_reset_minute"`
	WeeklyResetWeekday *int `json:"weekly_reset_weekday"`
	WeeklyResetHour    *int `json:"weekly_reset_hour"`
	WeeklyResetMinute  *int `json:"weekly_reset_minute"`
	MonthlyResetDay    *int `json:"monthly_reset_day"`
	MonthlyResetHour   *int `json:"monthly_reset_hour"`
	MonthlyResetMinute *int `json:"monthly_reset_minute"`
}

func (r updateBudgetSettingsRequest) apply(settings budget.Settings) budget.Settings {
	if r.DailyResetHour != nil {
		settings.DailyResetHour = *r.DailyResetHour
	}
	if r.DailyResetMinute != nil {
		settings.DailyResetMinute = *r.DailyResetMinute
	}
	if r.WeeklyResetWeekday != nil {
		settings.WeeklyResetWeekday = *r.WeeklyResetWeekday
	}
	if r.WeeklyResetHour != nil {
		settings.WeeklyResetHour = *r.WeeklyResetHour
	}
	if r.WeeklyResetMinute != nil {
		settings.WeeklyResetMinute = *r.WeeklyResetMinute
	}
	if r.MonthlyResetDay != nil {
		settings.MonthlyResetDay = *r.MonthlyResetDay
	}
	if r.MonthlyResetHour != nil {
		settings.MonthlyResetHour = *r.MonthlyResetHour
	}
	if r.MonthlyResetMinute != nil {
		settings.MonthlyResetMinute = *r.MonthlyResetMinute
	}
	return settings
}

type resetBudgetsRequest struct {
	Confirmation string `json:"confirmation"`
	Confirm      string `json:"confirm,omitempty"`
}

func (r resetBudgetsRequest) confirmationValue() string {
	if strings.TrimSpace(r.Confirmation) != "" {
		return r.Confirmation
	}
	return r.Confirm
}

type resetBudgetsResponse struct {
	Status string `json:"status"`
}

func budgetStatusResponses(statuses []budget.CheckResult, now time.Time) []budgetStatusResponse {
	if len(statuses) == 0 {
		return []budgetStatusResponse{}
	}
	responses := make([]budgetStatusResponse, 0, len(statuses))
	for _, status := range statuses {
		item := status.Budget
		responses = append(responses, budgetStatusResponse{
			UserPath:      item.UserPath,
			PeriodSeconds: item.PeriodSeconds,
			PeriodLabel:   budget.PeriodLabel(item.PeriodSeconds),
			Amount:        item.Amount,
			Source:        item.Source,
			LastResetAt:   item.LastResetAt,
			CreatedAt:     item.CreatedAt,
			UpdatedAt:     item.UpdatedAt,
			PeriodStart:   status.PeriodStart,
			PeriodEnd:     status.PeriodEnd,
			Spent:         status.Spent,
			HasUsage:      status.HasUsage,
			Remaining:     status.Remaining,
			UsageRatio:    status.UsageRatio(),
			PeriodRatio:   status.PeriodRatio(now),
		})
	}
	return responses
}

func budgetRequestKey(rawUserPath string, key *budgetKeyRequest) (string, int64, error) {
	userPath, err := budget.NormalizeUserPath(rawUserPath)
	if err != nil {
		return "", 0, err
	}
	periodSeconds, err := budgetKeyPeriodSeconds(key)
	if err != nil {
		return "", 0, err
	}
	return userPath, periodSeconds, nil
}

func budgetKeyPeriodSeconds(key *budgetKeyRequest) (int64, error) {
	if key == nil {
		return 0, errors.New("budget_key is required")
	}
	period := strings.TrimSpace(key.Period)
	hasPeriod := period != ""
	hasPeriodSeconds := key.PeriodSeconds != 0
	if !hasPeriod && !hasPeriodSeconds {
		return 0, errors.New("budget_key.period or budget_key.period_seconds is required")
	}
	if hasPeriod && hasPeriodSeconds {
		return 0, errors.New("set either budget_key.period or budget_key.period_seconds, not both")
	}
	if key.PeriodSeconds < 0 {
		return 0, errors.New("budget_key.period_seconds must be greater than 0")
	}
	return budgetRequestPeriodSeconds(period, key.PeriodSeconds)
}

func budgetRequestPeriodSeconds(period string, periodSeconds int64) (int64, error) {
	if periodSeconds > 0 {
		return periodSeconds, nil
	}
	period = strings.TrimSpace(period)
	if parsed, ok := budget.PeriodSeconds(period); ok {
		return parsed, nil
	}
	if parsed, err := strconv.ParseInt(period, 10, 64); err == nil {
		if parsed <= 0 {
			return 0, errors.New("period_seconds must be greater than 0")
		}
		return parsed, nil
	}
	return 0, errors.New("period must be one of hourly, daily, weekly, monthly or period_seconds must be set")
}
