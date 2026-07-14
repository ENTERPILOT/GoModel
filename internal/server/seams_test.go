package server

// Test-only convenience wrappers over the production constructors and
// middleware. Production wires the fuller variants directly
// (newHandlerWithAuthorizer at http.go, AuthMiddlewareWithAuthenticator,
// WorkflowResolutionWithResolverAndPolicy); tests use these to avoid
// repeating nil arguments.

import (
	"io"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// NewHandler creates a handler with the given routable provider and no
// optional resolvers.
func NewHandler(provider core.RoutableProvider, logger auditlog.LoggerInterface, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver) *Handler {
	return newHandler(provider, logger, usageLogger, pricingResolver, nil, nil, nil, nil)
}

func newHandler(
	provider core.RoutableProvider,
	logger auditlog.LoggerInterface,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
	modelResolver RequestModelResolver,
	workflowPolicyResolver RequestWorkflowPolicyResolver,
	failoverResolver RequestFailoverResolver,
	translatedRequestPatcher TranslatedRequestPatcher,
) *Handler {
	return newHandlerWithAuthorizer(
		provider,
		logger,
		usageLogger,
		pricingResolver,
		modelResolver,
		nil,
		workflowPolicyResolver,
		failoverResolver,
		translatedRequestPatcher,
	)
}

// AuthMiddleware validates the master key without a managed-key authenticator.
func AuthMiddleware(masterKey string, skipPaths []string) echo.MiddlewareFunc {
	return AuthMiddlewareWithAuthenticator(masterKey, nil, skipPaths)
}

// WorkflowResolution resolves request-scoped workflows without an explicit
// selector resolver or policy resolver.
func WorkflowResolution(provider core.RoutableProvider) echo.MiddlewareFunc {
	return WorkflowResolutionWithResolverAndPolicy(provider, nil, nil)
}

// WorkflowResolutionWithResolver resolves request-scoped workflows using an
// explicit selector resolver when provided.
func WorkflowResolutionWithResolver(provider core.RoutableProvider, resolver RequestModelResolver) echo.MiddlewareFunc {
	return WorkflowResolutionWithResolverAndPolicy(provider, resolver, nil)
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

// handleStreamingResponse obtains the stream from streamFn and relays it,
// recording dispatch errors the way the live inference paths do before they
// call handleStreamingReadCloser.
func (s *translatedInferenceService) handleStreamingResponse(
	c *echo.Context,
	workflow *core.Workflow,
	model, provider, providerName string,
	streamFn func() (io.ReadCloser, error),
) error {
	stream, err := streamFn()
	if err != nil {
		return handleStreamingDispatchError(c, err)
	}
	return s.handleStreamingReadCloser(c, workflow, model, provider, providerName, "", stream, nil)
}
