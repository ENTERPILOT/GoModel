package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
)

type BudgetChecker interface {
	Check(ctx context.Context, subjects budget.Subjects, now time.Time) error
}

// budgetSubjectsFromContext reads the dimensions a request can be budgeted by:
// its user path and the labels tagging extracted at ingress.
func budgetSubjectsFromContext(ctx context.Context) budget.Subjects {
	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}
	return budget.Subjects{UserPath: userPath, Labels: core.RequestLabelsFromContext(ctx)}
}

func enforceBudget(c *echo.Context, checker BudgetChecker) error {
	if checker == nil || c == nil || c.Request() == nil {
		return nil
	}
	return enforceBudgetForContext(c.Request().Context(), checker)
}

func enforceBudgetForContext(ctx context.Context, checker BudgetChecker) error {
	if checker == nil || ctx == nil {
		return nil
	}
	if workflow := core.GetWorkflow(ctx); workflow != nil && !workflow.BudgetEnabled() {
		return nil
	}
	if err := checker.Check(ctx, budgetSubjectsFromContext(ctx), time.Now().UTC()); err != nil {
		return budgetCheckError(err)
	}
	return nil
}

func budgetCheckError(err error) error {
	if exceeded, ok := errors.AsType[*budget.ExceededError](err); ok {
		message := exceeded.Error()
		if message == "" {
			message = "budget exceeded"
		}
		gatewayErr := core.NewRateLimitError("budget", message).WithCode("budget_exceeded")
		if retryAfter := budgetRetryAfterHeader(exceeded.Result.PeriodEnd, time.Now().UTC()); retryAfter != "" {
			return &gatewayErrorWithResponseHeaders{
				GatewayError: gatewayErr,
				headers:      http.Header{"Retry-After": []string{retryAfter}},
			}
		}
		return gatewayErr
	}
	return core.NewProviderError("budget", http.StatusServiceUnavailable, "budget check failed", err).
		WithCode("budget_check_failed")
}

type gatewayErrorWithResponseHeaders struct {
	*core.GatewayError
	headers http.Header
}

func (e *gatewayErrorWithResponseHeaders) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.GatewayError
}

func (e *gatewayErrorWithResponseHeaders) ResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return e.headers.Clone()
}

func budgetRetryAfterHeader(periodEnd time.Time, now time.Time) string {
	if periodEnd.IsZero() {
		return ""
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	delay := periodEnd.UTC().Sub(now.UTC())
	if delay <= 0 {
		return "0"
	}
	return strconv.FormatInt(int64(math.Ceil(delay.Seconds())), 10)
}
