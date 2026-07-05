package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/authkeys"
	"gomodel/internal/budget"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/pricingoverrides"
	"gomodel/internal/ratelimit"
	"gomodel/internal/virtualmodels"
	"gomodel/internal/workflows"
)

func budgetServiceError(message string, err error) error {
	if errors.Is(err, budget.ErrNotFound) {
		return core.NewNotFoundError("budget not found").WithCode("budget_not_found")
	}
	return core.NewProviderError("budgets", http.StatusServiceUnavailable, message, err)
}

func rateLimitServiceError(message string, err error) error {
	if errors.Is(err, ratelimit.ErrNotFound) {
		return core.NewNotFoundError("rate limit rule not found").WithCode("rate_limit_not_found")
	}
	return core.NewProviderError("ratelimits", http.StatusServiceUnavailable, message, err)
}

func featureUnavailableError(message string) error {
	return core.NewInvalidRequestErrorWithStatus(http.StatusServiceUnavailable, message, nil).
		WithCode("feature_unavailable")
}

func validationWriter(isValidation func(error) bool) func(error) error {
	return func(err error) error {
		if err == nil {
			return nil
		}
		if isValidation(err) {
			return core.NewInvalidRequestError(err.Error(), err)
		}
		return err
	}
}

var (
	workflowWriteError  = validationWriter(workflows.IsValidationError)
	authKeyWriteError   = validationWriter(authkeys.IsValidationError)
	guardrailWriteError = validationWriter(guardrails.IsValidationError)
)

// virtualModelWriteError surfaces validation errors as 400 and other failures
// as 502 so the dashboard distinguishes store/provider failures from input issues.
func virtualModelWriteError(err error) error {
	if err == nil {
		return nil
	}
	if virtualmodels.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("virtual_models", http.StatusBadGateway, err.Error(), err)
}

func pricingOverrideWriteError(err error) error {
	if err == nil {
		return nil
	}
	if pricingoverrides.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("model_pricing_overrides", http.StatusBadGateway, err.Error(), err)
}

func deactivateByID(
	c *echo.Context,
	unavailableErr error,
	idLabel string,
	notFoundErr error,
	notFoundMessage string,
	deactivate func(context.Context, string) error,
	writeError func(error) error,
) error {
	if unavailableErr != nil {
		return handleError(c, unavailableErr)
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError(idLabel+" id is required", nil))
	}

	if err := deactivate(c.Request().Context(), id); err != nil {
		if errors.Is(err, notFoundErr) {
			return handleError(c, core.NewNotFoundError(notFoundMessage+id))
		}
		return handleError(c, writeError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// modelPricingOverrideSelectorMaxLen caps decoded selectors to a sane size; provider
// IDs and model IDs are short identifiers, never essays.
const modelPricingOverrideSelectorMaxLen = 256

func normalizeModelPricingOverrideSelector(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", core.NewInvalidRequestError("model pricing override selector is required", nil)
	}
	if len(selector) > modelPricingOverrideSelectorMaxLen {
		return "", core.NewInvalidRequestError("model pricing override selector is too long", nil)
	}
	return selector, nil
}
