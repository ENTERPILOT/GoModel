package app

import (
	"gomodel/internal/providers"
	"gomodel/internal/ratelimit"
)

// rateLimitAwareCatalog narrows the registry's ModelAvailable with live
// provider/model rate-limit capacity, so virtual-model load balancing and
// failover selection route around saturated targets the same way they route
// around providers with stale inventory. All other catalog reads delegate to
// the embedded registry.
type rateLimitAwareCatalog struct {
	*providers.ModelRegistry
	limiter *ratelimit.Service
}

func newRateLimitAwareCatalog(registry *providers.ModelRegistry, limiter *ratelimit.Service) rateLimitAwareCatalog {
	return rateLimitAwareCatalog{ModelRegistry: registry, limiter: limiter}
}

func (c rateLimitAwareCatalog) ModelAvailable(model string) bool {
	if !c.ModelRegistry.ModelAvailable(model) {
		return false
	}
	return c.limiter.RouteAvailable(c.GetProviderName(model), model)
}
