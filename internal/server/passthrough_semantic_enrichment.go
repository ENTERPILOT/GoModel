package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// PassthroughSemanticEnrichment applies provider-owned passthrough metadata
// enrichment before workflow resolution runs.
func PassthroughSemanticEnrichment(provider core.RoutableProvider, enrichers []core.PassthroughSemanticEnricher, allowPassthroughV1Alias bool) echo.MiddlewareFunc {
	byProvider := make(map[string]core.PassthroughSemanticEnricher, len(enrichers))
	for _, enricher := range enrichers {
		if enricher == nil {
			continue
		}
		providerType := strings.TrimSpace(enricher.ProviderType())
		if providerType == "" {
			continue
		}
		byProvider[providerType] = enricher
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c == nil || core.DescribeEndpointPath(c.Request().URL.Path).Operation != core.OperationProviderPassthrough {
				return next(c)
			}

			env := ensureWhiteBoxPrompt(c)
			info := passthroughRouteInfo(c)
			if env == nil || info == nil {
				return next(c)
			}

			normalized, err := normalizePassthroughEndpoint(info.RawEndpoint, allowPassthroughV1Alias)
			if err != nil {
				return next(c)
			}
			info.NormalizedEndpoint = normalized
			if resolved := resolvePassthroughProvider(provider, info.Provider); resolved.ProviderType != "" {
				info.Provider = resolved.ProviderType
			}

			if enricher := byProvider[strings.TrimSpace(info.Provider)]; enricher != nil {
				if enriched := enricher.Enrich(core.GetRequestSnapshot(c.Request().Context()), env, info); enriched != nil {
					info = enriched
				}
			}

			core.CachePassthroughRouteInfo(env, info)
			return next(c)
		}
	}
}
