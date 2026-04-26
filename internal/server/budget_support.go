package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil && !workflow.BudgetEnabled() {
		return nil
	}
	userPath := core.UserPathFromContext(c.Request().Context())
	if userPath == "" {
		userPath = "/"
	}
	if err := checker.Check(c.Request().Context(), userPath, time.Now().UTC()); err != nil {
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
		return core.NewRateLimitError("budget", message).WithCode("budget_exceeded")
	}
	return core.NewProviderError("budget", http.StatusServiceUnavailable, fmt.Sprintf("budget check failed: %v", err), err).
		WithCode("budget_check_failed")
}
