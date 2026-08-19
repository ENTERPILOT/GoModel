package server

import (
	"bytes"
	// encoding/json rather than goccy: rewriteMessagesModel needs the
	// decoder's InputOffset to splice the model value in place.
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
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
	// Request rewriters (ext extensions) that asked for response feedback
	// observe the native SSE stream too; its Anthropic-native usage events
	// are understood by the feedback observer.
	var extraObservers []streaming.Observer
	if hasResponseFeedbackObservers(c) {
		extraObservers = append(extraObservers, &responseFeedbackStreamObserver{
			ctx:          c.Request().Context(),
			observers:    responseFeedbackObservers(c),
			requestID:    requestIDFromContextOrHeader(c.Request()),
			sessionID:    core.SessionIDFromContext(c.Request().Context()),
			endpoint:     ext.Endpoint("/v1/messages"),
			model:        req.Model,
			providerType: anthropicProviderType,
			providerName: providerName,
		})
	}
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, anthropicProviderType, providerName, "messages", info, resp, extraObservers...)
}

// rewriteMessagesModel returns body with its top-level "model" value replaced
// by the resolved model so aliased/renamed models reach the provider under
// their real name. Only the model value's bytes are spliced; every other byte
// of the request is preserved. The body is returned unchanged when the model
// already matches or has no model field (upstream validation rejects that).
func rewriteMessagesModel(body []byte, model string) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return body, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("request body is not a JSON object")
	}
	// Walk every top-level member and remember the span of the last "model"
	// value: decoders keep the last duplicate member, so that is the one the
	// resolved model came from and the one to rewrite.
	var modelRaw json.RawMessage
	var modelEnd int64
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if key != "model" {
			continue
		}
		modelRaw = raw
		modelEnd = dec.InputOffset()
	}
	if modelRaw == nil {
		return body, nil
	}
	var current string
	_ = json.Unmarshal(modelRaw, &current)
	if current == model {
		return body, nil
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	// The model value is a scalar, so modelRaw holds its exact source bytes
	// and modelEnd points just past them.
	start := modelEnd - int64(len(modelRaw))
	rewritten := make([]byte, 0, int64(len(body))-int64(len(modelRaw))+int64(len(encoded)))
	rewritten = append(rewritten, body[:start]...)
	rewritten = append(rewritten, encoded...)
	rewritten = append(rewritten, body[modelEnd:]...)
	return rewritten, nil
}
