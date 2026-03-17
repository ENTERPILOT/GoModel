package server

import (
	"encoding/json"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type modelCountProvider interface {
	ModelCount() int
}

// ExecutionPlanning resolves the request-scoped execution plan for model-facing
// routes. The plan centralizes endpoint capabilities, execution mode, resolved
// provider type, and any early model routing decision that downstream handlers
// or middleware need to consume.
func ExecutionPlanning(provider core.RoutableProvider) echo.MiddlewareFunc {
	return ExecutionPlanningWithResolver(provider, nil)
}

// ExecutionPlanningWithResolver resolves request-scoped execution plans using
// an explicit selector resolver when provided. This lets request planning own
// alias policy instead of depending on provider decorators.
func ExecutionPlanningWithResolver(provider core.RoutableProvider, resolver RequestModelResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if !core.IsModelInteractionPath(path) {
				return next(c)
			}
			plan, err := deriveExecutionPlan(c, provider, resolver)
			if err != nil {
				return handleError(c, err)
			}
			if plan != nil {
				storeExecutionPlan(c, plan)
			}
			return next(c)
		}
	}
}

func deriveExecutionPlan(c *echo.Context, provider core.RoutableProvider, resolver RequestModelResolver) (*core.ExecutionPlan, error) {
	if c == nil {
		return nil, nil
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	if requestID != "" && strings.TrimSpace(core.GetRequestID(c.Request().Context())) != requestID {
		c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), requestID)))
	}

	desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
	plan := &core.ExecutionPlan{
		RequestID:    requestID,
		Endpoint:     desc,
		Capabilities: core.CapabilitiesForEndpoint(desc),
	}

	switch desc.Operation {
	case core.OperationProviderPassthrough:
		passthrough := passthroughRouteInfo(c)
		providerType, ok := providerPassthroughType(c)
		if !ok {
			return nil, nil
		}
		if passthrough == nil {
			passthrough = &core.PassthroughRouteInfo{}
		}
		if strings.TrimSpace(passthrough.Provider) == "" {
			cloned := *passthrough
			cloned.Provider = providerType
			passthrough = &cloned
		}
		plan.Mode = core.ExecutionModePassthrough
		plan.ProviderType = providerType
		plan.Passthrough = passthrough
		auditlog.EnrichEntryWithExecutionPlan(c, plan)
		return plan, nil

	case core.OperationBatches:
		plan.Mode = core.ExecutionModeNativeBatch
		return plan, nil

	case core.OperationFiles:
		plan.Mode = core.ExecutionModeNativeFile
		return plan, nil

	case core.OperationChatCompletions, core.OperationResponses, core.OperationEmbeddings:
		plan.Mode = core.ExecutionModeTranslated
		resolution, parsed, err := ensureRequestModelResolution(c, provider, resolver)
		if err != nil {
			return nil, err
		}
		if !parsed || resolution == nil {
			return plan, nil
		}
		plan.ProviderType = resolution.ProviderType
		plan.Resolution = resolution
		auditlog.EnrichEntryWithExecutionPlan(c, plan)
		return plan, nil

	default:
		return nil, nil
	}
}

func storeExecutionPlan(c *echo.Context, plan *core.ExecutionPlan) {
	if c == nil || plan == nil {
		return
	}
	ctx := core.WithExecutionPlan(c.Request().Context(), plan)
	c.SetRequest(c.Request().WithContext(ctx))
}

func selectorHintsForValidation(c *echo.Context) (model, provider string, parsed bool, err error) {
	ctx := c.Request().Context()
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		if model, provider, ok := cachedCanonicalSelectorHints(env); ok {
			return model, provider, true, nil
		}
		if env.JSONBodyParsed || env.RouteHints.Model != "" || env.RouteHints.Provider != "" {
			return env.RouteHints.Model, env.RouteHints.Provider, true, nil
		}
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return "", "", false, err
	}
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		if model, provider, ok := core.DecodeCanonicalSelector(bodyBytes, env); ok {
			return model, provider, true, nil
		}
	}

	var peek struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(bodyBytes, &peek); err != nil {
		return "", "", false, nil
	}
	return peek.Model, peek.Provider, true, nil
}

func cachedCanonicalSelectorHints(env *core.WhiteBoxPrompt) (model, provider string, ok bool) {
	return env.CanonicalSelectorFromCachedRequest()
}

func providerPassthroughType(c *echo.Context) (string, bool) {
	if info := passthroughRouteInfo(c); info != nil {
		providerType := strings.TrimSpace(info.Provider)
		if providerType != "" {
			return providerType, true
		}
	}
	if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil && env.OperationType == string(core.OperationProviderPassthrough) {
		providerType := strings.TrimSpace(env.RouteHints.Provider)
		if providerType != "" {
			return providerType, true
		}
	}
	if providerType, _, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path); ok {
		return providerType, true
	}
	return "", false
}

func passthroughRouteInfo(c *echo.Context) *core.PassthroughRouteInfo {
	if c == nil {
		return nil
	}
	if plan := core.GetExecutionPlan(c.Request().Context()); plan != nil && plan.Passthrough != nil {
		if plan.Passthrough.Provider == "" && strings.TrimSpace(plan.ProviderType) != "" {
			plan.Passthrough.Provider = strings.TrimSpace(plan.ProviderType)
		}
		if plan.Passthrough.AuditPath == "" {
			plan.Passthrough.AuditPath = c.Request().URL.Path
		}
		return plan.Passthrough
	}
	if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil {
		if info := env.CachedPassthroughRouteInfo(); info != nil {
			if info.AuditPath == "" {
				info.AuditPath = c.Request().URL.Path
			}
			return info
		}
		if env.OperationType == string(core.OperationProviderPassthrough) {
			info := &core.PassthroughRouteInfo{
				Provider:    env.RouteHints.Provider,
				RawEndpoint: env.RouteHints.Endpoint,
				Model:       env.RouteHints.Model,
				AuditPath:   c.Request().URL.Path,
			}
			if info.Provider != "" || info.RawEndpoint != "" || info.Model != "" {
				return info
			}
		}
	}
	provider, endpoint, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path)
	if !ok {
		return nil
	}
	return &core.PassthroughRouteInfo{
		Provider:    provider,
		RawEndpoint: endpoint,
		AuditPath:   c.Request().URL.Path,
	}
}

// GetProviderType returns the provider type captured in the execution plan for this request.
func GetProviderType(c *echo.Context) string {
	if plan := core.GetExecutionPlan(c.Request().Context()); plan != nil {
		if providerType := strings.TrimSpace(plan.ProviderType); providerType != "" {
			return providerType
		}
	}
	return ""
}
