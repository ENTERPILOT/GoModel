package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/budget"
	"gomodel/internal/core"
)

type BudgetChecker interface {
	Check(ctx context.Context, userPath string, now time.Time) error
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
	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}
	if err := checker.Check(ctx, userPath, time.Now().UTC()); err != nil {
		return budgetCheckError(err)
	}
	return nil
}

func budgetCheckError(err error) error {
	var exceeded *budget.ExceededError
	if errors.As(err, &exceeded) {
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
	return core.NewProviderError("budget", http.StatusServiceUnavailable, fmt.Sprintf("budget check failed: %v", err), err).
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
