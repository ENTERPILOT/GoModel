package server

import (
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tidwall/gjson"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// WorkflowResolution resolves the request-scoped workflow for model-facing
// routes. The workflow centralizes endpoint capabilities, execution mode, resolved
// provider type, and any early model routing decision that downstream handlers
// or middleware need to consume.
func WorkflowResolution(provider core.RoutableProvider) echo.MiddlewareFunc {
	return WorkflowResolutionWithResolverAndPolicy(provider, nil, nil)
}

// WorkflowResolutionWithResolver resolves request-scoped workflows using
// an explicit selector resolver when provided. This lets workflow resolution own
// alias policy instead of depending on provider decorators.
func WorkflowResolutionWithResolver(provider core.RoutableProvider, resolver RequestModelResolver) echo.MiddlewareFunc {
	return WorkflowResolutionWithResolverAndPolicy(provider, resolver, nil)
}

// WorkflowResolutionWithResolverAndPolicy resolves request-scoped workflows
// and matches one persisted workflow policy when configured.
func WorkflowResolutionWithResolverAndPolicy(
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestWorkflowPolicyResolver,
) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if !core.IsModelInteractionPath(path) {
				return next(c)
			}
			workflow, err := deriveWorkflowWithPolicy(c, provider, resolver, policyResolver)
			if err != nil {
				return handleError(c, err)
			}
			if workflow != nil {
				storeWorkflow(c, workflow)
			}
			return next(c)
		}
	}
}

func deriveWorkflowWithPolicy(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestWorkflowPolicyResolver,
) (*core.Workflow, error) {
	if c == nil {
		return nil, nil
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	if requestID != "" && strings.TrimSpace(core.GetRequestID(c.Request().Context())) != requestID {
		c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), requestID)))
	}

	desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
	userPath := core.UserPathFromContext(c.Request().Context())
	workflow := &core.Workflow{
		RequestID:    requestID,
		Endpoint:     desc,
		Capabilities: core.CapabilitiesForEndpoint(desc),
	}

	switch desc.Operation {
	case core.OperationProviderPassthrough:
		passthrough := passthroughRouteInfo(c)
		providerType, providerName, ok := providerPassthroughType(c, provider)
		if !ok {
			return nil, nil
		}
		selectorSource := passthrough
		if passthrough == nil {
			passthrough = &core.PassthroughRouteInfo{}
		}
		cloned := *passthrough
		cloned.Provider = providerType
		passthrough = &cloned
		workflow.Mode = core.ExecutionModePassthrough
		workflow.ProviderType = providerType
		workflow.Passthrough = passthrough
		policySelector := core.NewWorkflowSelector(providerName, passthrough.Model, userPath)
		if sel, hasSel := passthroughAccessSelector(provider, selectorSource); hasSel {
			policySelector = core.NewWorkflowSelector(sel.Provider, sel.Model, userPath)
		}
		if err := applyWorkflowPolicy(
			c.Request().Context(),
			workflow,
			policyResolver,
			policySelector,
		); err != nil {
			return nil, err
		}
		return workflow, nil

	case core.OperationBatches:
		workflow.Mode = core.ExecutionModeNativeBatch
		if err := applyWorkflowPolicy(c.Request().Context(), workflow, policyResolver, core.WorkflowSelector{}); err != nil {
			return nil, err
		}
		return workflow, nil

	case core.OperationFiles:
		workflow.Mode = core.ExecutionModeNativeFile
		if err := applyWorkflowPolicy(c.Request().Context(), workflow, policyResolver, core.WorkflowSelector{}); err != nil {
			return nil, err
		}
		return workflow, nil

	case core.OperationChatCompletions, core.OperationResponses, core.OperationEmbeddings:
		workflow.Mode = core.ExecutionModeTranslated
		resolution, parsed, err := ensureRequestModelResolution(c, provider, resolver)
		if err != nil {
			return nil, err
		}
		if !parsed || resolution == nil {
			if err := applyWorkflowPolicy(c.Request().Context(), workflow, policyResolver, core.WorkflowSelector{}); err != nil {
				return nil, err
			}
			return workflow, nil
		}
		return translatedWorkflow(c.Request().Context(), requestID, desc, resolution, policyResolver)

	default:
		return nil, nil
	}
}

func storeWorkflow(c *echo.Context, workflow *core.Workflow) {
	if c == nil || workflow == nil {
		return
	}
	auditlog.EnrichEntryWithWorkflow(c, workflow)
	ctx := core.WithWorkflow(c.Request().Context(), workflow)
	c.SetRequest(c.Request().WithContext(ctx))
}

func selectorHintsForValidation(c *echo.Context) (model, provider string, parsed bool, err error) {
	ctx := c.Request().Context()
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		if model, provider, ok := cachedCanonicalSelectorHints(env); ok {
			return model, provider, true, nil
		}
		if env.JSONBodyParsed || env.RouteHints.Provider != "" {
			return env.RouteHints.Model, env.RouteHints.Provider, true, nil
		}
	}

	if hints := peekRequestBodySelectorHints(c.Request(), requestSelectorPeekLimit); hints.parsed && (hints.complete || hints.provider != "") {
		return hints.model, hints.provider, true, nil
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

	model, provider, ok := selectorHintsFromJSONGJSON(bodyBytes)
	return model, provider, ok, nil
}

func cachedCanonicalSelectorHints(env *core.WhiteBoxPrompt) (model, provider string, ok bool) {
	return env.CanonicalSelectorFromCachedRequest()
}

func selectorHintsFromJSONGJSON(body []byte) (model, provider string, parsed bool) {
	if !gjson.ValidBytes(body) {
		return "", "", false
	}

	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "", "", false
	}

	// gjson returns the first matching top-level field. That differs from
	// encoding/json on duplicate keys, but the hot-path speedup is worth it here:
	// duplicate selector keys are not expected from real clients, and we accept
	// the first-match behavior to keep workflow resolution fast.
	modelResult := root.Get("model")
	if !selectorHintValueAllowed(modelResult) {
		return "", "", false
	}
	providerResult := root.Get("provider")
	if !selectorHintValueAllowed(providerResult) {
		return "", "", false
	}

	if modelResult.Type == gjson.String {
		model = modelResult.String()
	}
	if providerResult.Type == gjson.String {
		provider = providerResult.String()
	}
	return model, provider, true
}

func selectorHintValueAllowed(result gjson.Result) bool {
	if !result.Exists() {
		return true
	}
	return result.Type == gjson.String || result.Type == gjson.Null
}

func providerPassthroughType(c *echo.Context, provider core.RoutableProvider) (string, string, bool) {
	if info := passthroughRouteInfo(c); info != nil {
		resolved := resolvePassthroughProvider(provider, info.Provider)
		if providerType := strings.TrimSpace(resolved.ProviderType); providerType != "" {
			return providerType, strings.TrimSpace(resolved.ProviderName), true
		}
	}
	if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil && env.OperationType == string(core.OperationProviderPassthrough) {
		resolved := resolvePassthroughProvider(provider, env.RouteHints.Provider)
		if providerType := strings.TrimSpace(resolved.ProviderType); providerType != "" {
			return providerType, strings.TrimSpace(resolved.ProviderName), true
		}
	}
	if routeProvider, _, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path); ok {
		resolved := resolvePassthroughProvider(provider, routeProvider)
		if providerType := strings.TrimSpace(resolved.ProviderType); providerType != "" {
			return providerType, strings.TrimSpace(resolved.ProviderName), true
		}
	}
	return "", "", false
}

func passthroughRouteInfo(c *echo.Context) *core.PassthroughRouteInfo {
	if c == nil {
		return nil
	}
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil && workflow.Passthrough != nil {
		if workflow.Passthrough.Provider == "" && strings.TrimSpace(workflow.ProviderType) != "" {
			workflow.Passthrough.Provider = strings.TrimSpace(workflow.ProviderType)
		}
		if workflow.Passthrough.AuditPath == "" {
			workflow.Passthrough.AuditPath = c.Request().URL.Path
		}
		return workflow.Passthrough
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

// GetProviderType returns the provider type captured in the workflow for this request.
func GetProviderType(c *echo.Context) string {
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil {
		if providerType := strings.TrimSpace(workflow.ProviderType); providerType != "" {
			return providerType
		}
	}
	return ""
}
