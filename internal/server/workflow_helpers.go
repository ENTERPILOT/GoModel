package server

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func ensureTranslatedRequestWorkflow(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestWorkflowPolicyResolver,
	model,
	providerHint *string,
) (*core.Workflow, error) {
	return ensureTranslatedRequestWorkflowWithAuthorizer(c, provider, resolver, nil, policyResolver, model, providerHint)
}

func ensureTranslatedRequestWorkflowWithAuthorizer(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	authorizer RequestModelAuthorizer,
	policyResolver RequestWorkflowPolicyResolver,
	model,
	providerHint *string,
) (*core.Workflow, error) {
	if model == nil || providerHint == nil {
		return nil, core.NewInvalidRequestError("model selector targets are required", nil)
	}

	workflow, err := ensureTranslatedWorkflow(c, provider, resolver, policyResolver)
	if err != nil {
		return nil, err
	}

	resolution := translatedWorkflowResolution(workflow)
	if resolution != nil && authorizer != nil {
		if err := authorizer.ValidateModelAccess(c.Request().Context(), resolution.ResolvedSelector); err != nil {
			return nil, err
		}
	}
	if resolution == nil {
		resolution, err = resolveAndStoreRequestModelResolution(c, provider, resolver, authorizer, *model, *providerHint)
		if err != nil {
			return nil, err
		}
		workflow, err = translatedWorkflowForRequest(c, resolution, policyResolver)
		if err != nil {
			return nil, err
		}
		storeWorkflow(c, workflow)
	}

	applyResolvedSelector(model, providerHint, resolution)
	return workflow, nil
}

func ensureTranslatedWorkflow(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestWorkflowPolicyResolver,
) (*core.Workflow, error) {
	if workflow := currentTranslatedWorkflow(c); workflow != nil {
		return workflow, nil
	}

	workflow, err := deriveWorkflowWithPolicy(c, provider, resolver, policyResolver)
	if err != nil || workflow == nil {
		return workflow, err
	}

	storeWorkflow(c, workflow)
	return core.GetWorkflow(c.Request().Context()), nil
}

func currentTranslatedWorkflow(c *echo.Context) *core.Workflow {
	if c == nil {
		return nil
	}
	workflow := core.GetWorkflow(c.Request().Context())
	if workflow == nil {
		return nil
	}

	desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
	if workflow.Mode != core.ExecutionModeTranslated || workflow.Endpoint.Operation != desc.Operation {
		return nil
	}
	return workflow
}

func translatedWorkflowResolution(workflow *core.Workflow) *core.RequestModelResolution {
	if workflow == nil {
		return nil
	}
	return workflow.Resolution
}

func applyResolvedSelector(model, providerHint *string, resolution *core.RequestModelResolution) {
	if model == nil || providerHint == nil || resolution == nil {
		return
	}
	*model = resolution.ResolvedSelector.Model
	*providerHint = resolution.ResolvedSelector.Provider
}

func translatedWorkflowForRequest(
	c *echo.Context,
	resolution *core.RequestModelResolution,
	policyResolver RequestWorkflowPolicyResolver,
) (*core.Workflow, error) {
	if c == nil {
		return nil, nil
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	ctx := c.Request().Context()
	if requestID != "" && strings.TrimSpace(core.GetRequestID(ctx)) != requestID {
		ctx = core.WithRequestID(ctx, requestID)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	return translatedWorkflow(
		c.Request().Context(),
		requestID,
		core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path),
		resolution,
		policyResolver,
	)
}

func translatedWorkflow(
	ctx context.Context,
	requestID string,
	endpoint core.EndpointDescriptor,
	resolution *core.RequestModelResolution,
	policyResolver RequestWorkflowPolicyResolver,
) (*core.Workflow, error) {
	workflow := &core.Workflow{
		RequestID:    strings.TrimSpace(requestID),
		Endpoint:     endpoint,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(endpoint),
	}
	if resolution != nil {
		workflow.ProviderType = strings.TrimSpace(resolution.ProviderType)
		workflow.Resolution = resolution
	}

	selector := core.WorkflowSelector{}
	if resolution != nil {
		selector = core.NewWorkflowSelector(
			resolvedWorkflowProviderName(resolution),
			resolution.ResolvedSelector.Model,
			core.UserPathFromContext(ctx),
		)
	}
	if err := applyWorkflowPolicy(ctx, workflow, policyResolver, selector); err != nil {
		return nil, err
	}
	return workflow, nil
}
