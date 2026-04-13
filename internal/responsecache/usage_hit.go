package responsecache

import (
	"log/slog"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

func newUsageHitRecorder(logger usage.LoggerInterface, pricingResolver usage.PricingResolver) func(*echo.Context, []byte, string) {
	if logger == nil || !logger.Config().Enabled {
		return nil
	}

	return func(c *echo.Context, body []byte, cacheType string) {
		if c == nil {
			return
		}

		ctx := c.Request().Context()
		plan := core.GetWorkflow(ctx)
		if plan != nil && !plan.UsageEnabled() {
			return
		}

		model := ""
		provider := ""
		providerName := ""
		if plan != nil {
			provider = strings.TrimSpace(plan.ProviderType)
			if plan.Resolution != nil {
				model = strings.TrimSpace(plan.Resolution.ResolvedSelector.Model)
				providerName = strings.TrimSpace(plan.Resolution.ProviderName)
			}
		}
		if provider == "" {
			slog.Debug("cache hit usage skipped: missing provider type")
			return
		}

		endpoint := c.Request().URL.Path
		requestID := core.GetRequestID(ctx)
		if requestID == "" {
			requestID = c.Request().Header.Get("X-Request-ID")
		}

		var pricing *core.ModelPricing
		if pricingResolver != nil {
			pricing = pricingResolver.ResolvePricing(model, provider)
		}

		entry := usage.ExtractFromCachedResponseBody(body, requestID, model, provider, endpoint, cacheType, pricing)
		if entry == nil {
			return
		}
		entry.ProviderName = providerName
		entry.UserPath = core.UserPathFromContext(ctx)
		logger.Write(entry)
	}
}
