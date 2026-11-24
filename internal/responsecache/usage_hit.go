package responsecache

import (
	"log/slog"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// hitRecorder prices a cache hit before it is replayed, so the response can
// carry its cost, and returns the function that records its usage once the
// replay is committed. The result is nil when the hit records no usage.
type hitRecorder func(ex exchange, body []byte, cacheType string) (record func())

// prepare prices the hit and returns the function that records its usage. It
// is safe to call on a nil recorder, and the result is never nil.
func (r hitRecorder) prepare(ex exchange, body []byte, cacheType string) func() {
	if r != nil {
		if record := r(ex, body, cacheType); record != nil {
			return record
		}
	}
	return func() {}
}

func newUsageHitRecorder(logger usage.LoggerInterface, pricingResolver usage.PricingResolver) hitRecorder {
	if logger == nil || !logger.Config().Enabled {
		return nil
	}

	return func(ex exchange, body []byte, cacheType string) func() {
		if ex == nil {
			return nil
		}

		ctx := ex.Context()
		plan := core.GetWorkflow(ctx)
		if plan != nil && !plan.UsageEnabled() {
			return nil
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
			return nil
		}

		endpoint := ex.Path()
		requestID := core.GetRequestID(ctx)
		if requestID == "" {
			requestID = ex.RequestHeader(core.RequestIDHeader)
		}

		pricing := usage.ResolveServedModelPricing(pricingResolver, model, cacheHitPricingProvider(provider, providerName),
			func() string { return cachedAnsweredModel(body) })

		entry := usage.ExtractFromCachedResponseBody(body, requestID, model, provider, endpoint, cacheType, pricing)
		if entry == nil {
			return nil
		}
		entry.ProviderName = providerName
		entry.RoutedModel = model
		entry.UserPath = core.UserPathFromContext(ctx)
		entry.SessionID = core.SessionIDFromContext(ctx)
		entry.AuthKeyID = core.GetAuthKeyID(ctx)
		entry.Labels = core.RequestLabelsFromContext(ctx)
		usage.ReportCost(ctx, logger, entry)
		return func() { logger.Write(entry) }
	}
}

// cachedAnsweredModel returns the model a cached JSON answer names, such as
// jev-1.13.0 for a request routed to jev-latest, or "" for other bodies.
func cachedAnsweredModel(body []byte) string {
	var answer struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ""
	}
	return answer.Model
}

func cacheHitPricingProvider(provider, providerName string) string {
	if name := strings.TrimSpace(providerName); name != "" {
		return name
	}
	return strings.TrimSpace(provider)
}
