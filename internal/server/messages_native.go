package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

const anthropicProviderType = "anthropic"

// canForwardMessagesNatively reports whether a prepared /v1/messages request
// can skip the translated pipeline and be forwarded to the provider in its
// original Anthropic dialect. Native forwarding preserves fields the canonical
// translation cannot round-trip (cache_control breakpoints, thinking block
// signatures, anthropic-beta headers), which Claude Code clients depend on.
// Features that operate on the canonical translated request take precedence:
// requests using guardrails patching, response caching, or failover stay on
// the translated pipeline.
func (s *translatedInferenceService) canForwardMessagesNatively(workflow *core.Workflow) bool {
	if workflow == nil || strings.TrimSpace(workflow.ProviderType) != anthropicProviderType {
		return false
	}
	if s.translatedRequestPatcher != nil {
		return false
	}
	if s.responseCache != nil && workflow.CacheEnabled() {
		return false
	}
	if len(s.inference().FailoverSelectors(workflow)) > 0 {
		return false
	}
	_, ok := s.provider.(core.RoutablePassthrough)
	return ok
}

// dispatchMessagesNative forwards the original Anthropic Messages body to the
// resolved Anthropic provider and relays the provider-native response (JSON or
// SSE) unchanged, with admission, audit, and streaming usage accounting.
func (s *translatedInferenceService) dispatchMessagesNative(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}

	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	body, err = rewriteMessagesModel(body, req.Model)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	s.observeLiveProviderAttempts(c, workflow)

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRouteFromWorkflow(workflow))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx := adm.dispatchContext(c.Request().Context())

	providerName := ""
	if workflow.Resolution != nil {
		providerName = workflow.Resolution.ProviderName
	}

	resp, err := passthroughProvider.Passthrough(ctx, anthropicProviderType, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "messages",
		Operation:    "anthropic.messages",
		Model:        req.Model,
		Stream:       req.Stream,
		Body:         io.NopCloser(bytes.NewReader(body)),
		Headers:      buildPassthroughHeaders(ctx, c.Request().Header),
		ProviderName: providerName,
	})
	if err != nil {
		return handleError(c, err)
	}

	auditlog.EnrichEntryWithWorkflow(c, workflow)
	info := &core.PassthroughRouteInfo{
		Provider:           anthropicProviderType,
		ProviderName:       providerName,
		NormalizedEndpoint: "messages",
		SemanticOperation:  "anthropic.messages",
		GenAIOperation:     "chat",
		Stream:             req.Stream,
		AuditPath:          "/v1/messages",
		Model:              req.Model,
	}
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, anthropicProviderType, providerName, "messages", info, resp)
}

// rewriteMessagesModel returns body with its "model" field replaced by the
// resolved model, leaving the body untouched when it already matches so
// aliased/renamed models reach the provider under their real name.
func rewriteMessagesModel(body []byte, model string) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return body, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	var current string
	if raw, ok := fields["model"]; ok {
		_ = json.Unmarshal(raw, &current)
	}
	if current == model {
		return body, nil
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	fields["model"] = encoded
	return json.Marshal(fields)
}
