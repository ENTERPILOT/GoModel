package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/realtime"
	"gomodel/internal/usage"
)

// responseDoneMarker gates realtime usage parsing: only frames containing a
// "response.done" event carry usage, so a cheap byte scan avoids JSON-parsing
// every audio delta on the relay hot path.
var responseDoneMarker = []byte(`"response.done"`)

// realtimeService adapts Echo requests to the realtime websocket reverse proxy.
// It stays a thin transport layer: validate, authorize, enforce budget, resolve
// the upstream target (credential injection lives in the provider), then relay
// frames verbatim. The realtime event schema is the wire format, so no request
// or response translation happens here.
type realtimeService struct {
	provider        core.RoutableProvider
	modelAuthorizer RequestModelAuthorizer
	budgetChecker   BudgetChecker
	rateLimiter     RateLimiter
	usageLogger     usage.LoggerInterface
	pricingResolver usage.PricingResolver
	enabled         bool
}

// realtimeRoute carries the resolved routing identity for one session, used to
// label its usage entries and lifecycle logs the same way audio does.
type realtimeRoute struct {
	model        string
	providerType string
	providerName string
	requestID    string
	userPath     string
	labels       []string
}

// Realtime handles GET /v1/realtime, routing by the model query parameter.
func (s *realtimeService) Realtime(c *echo.Context) error {
	return s.handle(c, strings.TrimSpace(c.QueryParam("model")), strings.TrimSpace(c.QueryParam("provider")))
}

// PassthroughRealtime handles a websocket upgrade on /p/{provider}/v1/realtime.
// It routes by the same model resolution as the typed route, pinning the provider
// named in the path as the resolution hint.
func (s *realtimeService) PassthroughRealtime(c *echo.Context, providerType string) error {
	return s.handle(c, strings.TrimSpace(c.QueryParam("model")), providerType)
}

// handle is the single realtime entry point: gate, resolve+authorize, dial, relay.
func (s *realtimeService) handle(c *echo.Context, model, providerHint string) error {
	if !s.enabled {
		return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "realtime sessions are disabled", nil))
	}
	router, ok := s.provider.(core.RealtimeRouter)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("realtime is not supported by the current provider router", nil))
	}
	if model == "" {
		return handleError(c, core.NewInvalidRequestError("model query parameter is required", nil))
	}

	ctx, route, err := s.prepare(c, model, providerHint)
	if err != nil {
		return handleError(c, err)
	}
	// The proxy call blocks for the whole websocket session, so the released
	// concurrency slot spans the session lifetime.
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	target, err := router.RealtimeTarget(ctx, &core.RealtimeRequest{Model: model, Provider: providerHint})
	if err != nil {
		return handleError(c, err)
	}
	return s.proxy(c, ctx, target, route)
}

// prepare resolves and authorizes the model, enforces budget, and stamps the
// request id. It mirrors audioService.prepare so realtime sessions are gated by
// the same model-access and budget rules as the other model endpoints.
func (s *realtimeService) prepare(c *echo.Context, model, providerHint string) (context.Context, realtimeRoute, error) {
	selector, err := core.ParseModelSelector(model, providerHint)
	if err != nil {
		return nil, realtimeRoute{}, core.NewInvalidRequestError(err.Error(), err)
	}
	if resolver, ok := s.provider.(selectorResolver); ok {
		resolved, _, resolveErr := resolver.ResolveModel(core.NewRequestedModelSelector(model, providerHint))
		if resolveErr != nil {
			return nil, realtimeRoute{}, resolveErr
		}
		selector = resolved
	}
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), selector); err != nil {
			return nil, realtimeRoute{}, err
		}
	}
	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return nil, realtimeRoute{}, err
	}
	auditlog.EnrichEntry(c, selector.Model, "")

	ctx, requestID := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))

	qualified := selector.QualifiedModel()
	route := realtimeRoute{
		model:        selector.Model,
		providerType: s.provider.GetProviderType(qualified),
		providerName: selector.Provider,
		requestID:    requestID,
		userPath:     core.UserPathFromContext(ctx),
		labels:       core.RequestLabelsFromContext(ctx),
	}
	if resolver, ok := s.provider.(core.ProviderNameResolver); ok {
		if name := resolver.GetProviderName(qualified); name != "" {
			route.providerName = name
		}
	}
	return ctx, route, nil
}

// proxy injects credentials, relays frames, and logs the session lifecycle. A
// pre-upgrade dial failure becomes a normal HTTP error; once upgraded the
// connection is hijacked and the outcome is logged.
func (s *realtimeService) proxy(c *echo.Context, ctx context.Context, target *core.RealtimeTarget, route realtimeRoute) error {
	if target == nil || strings.TrimSpace(target.URL) == "" {
		return handleError(c, core.NewProviderError(route.providerType, http.StatusBadGateway, "provider returned no realtime target", nil))
	}

	t := realtime.Target{
		URL:          target.URL,
		Headers:      realtimeUpstreamHeaders(ctx, c.Request().Header, target.Headers),
		Subprotocols: target.Subprotocols,
	}

	slog.Info("realtime session opened", "request_id", route.requestID, "model", route.model, "provider", route.providerType)
	err := realtime.Proxy(c.Response(), c.Request(), t, s.usageTap(ctx, route))

	var de *realtime.DialError
	if errors.As(err, &de) {
		slog.Warn("realtime upstream dial failed", "request_id", route.requestID, "provider", route.providerType, "error", de.Err)
		return handleError(c, core.NewProviderError(route.providerType, http.StatusBadGateway, "failed to connect to realtime upstream", de.Err))
	}
	if err != nil {
		slog.Warn("realtime session closed with error", "request_id", route.requestID, "error", err)
		return nil // connection already hijacked; nothing to write
	}
	slog.Info("realtime session closed", "request_id", route.requestID)
	return nil
}

// usageTap returns a frame observer that records one usage entry per realtime
// "response.done" event, or nil when usage tracking is off (so the proxy skips
// the tap entirely). It honors both the global usage setting and per-workflow
// usage policy, mirroring the other streaming paths. usageLogger.Write is
// non-blocking, so the tap runs inline.
func (s *realtimeService) usageTap(ctx context.Context, route realtimeRoute) func([]byte) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled {
		return nil
	}
	if workflow := core.GetWorkflow(ctx); workflow != nil && !workflow.UsageEnabled() {
		return nil
	}
	return func(frame []byte) {
		if !bytes.Contains(frame, responseDoneMarker) {
			return
		}
		var pricing *core.ModelPricing
		if s.pricingResolver != nil {
			provider := route.providerName
			if provider == "" {
				provider = route.providerType
			}
			pricing = s.pricingResolver.ResolvePricing(route.model, provider)
		}
		entry := usage.ExtractFromRealtimeResponseDone(frame, route.requestID, route.model, route.providerType, pricing)
		if entry == nil {
			return
		}
		entry.ProviderName = strings.TrimSpace(route.providerName)
		entry.UserPath = route.userPath
		entry.Labels = route.labels
		s.usageLogger.Write(entry)
	}
}

// realtimeUpstreamHeaders builds the upstream handshake headers: forward the
// client's safe headers (auth and hop-by-hop already stripped), drop the
// websocket handshake headers the dialer regenerates, then overlay the provider
// target headers so injected credentials win.
func realtimeUpstreamHeaders(ctx context.Context, clientHeaders, target http.Header) http.Header {
	h := buildPassthroughHeaders(ctx, clientHeaders)
	if h == nil {
		h = http.Header{}
	}
	for key := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(key), "Sec-Websocket") {
			h.Del(key)
		}
	}
	// Drop the legacy realtime beta header: the GA endpoint rejects it, so a
	// client that still sends it would otherwise turn a valid session into an
	// upstream dial failure. Providers that need it set it via the target.
	h.Del("OpenAI-Beta")
	for key, values := range target {
		h.Del(key)
		for _, value := range values {
			h.Add(key, value)
		}
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// isWebSocketUpgrade reports whether the request is a websocket upgrade handshake
// (Connection: Upgrade + Upgrade: websocket, case-insensitive).
func isWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for token := range strings.SplitSeq(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}
