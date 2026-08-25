package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Usage markers gate realtime usage parsing: conversation sessions report usage
// in "response.done" events, transcription sessions in
// "conversation.item.input_audio_transcription.completed" events. A cheap byte
// scan avoids JSON-parsing every audio delta on the relay hot path.
var (
	responseDoneMarker       = []byte(`"response.done"`)
	transcriptionUsageMarker = []byte(`"conversation.item.input_audio_transcription.completed"`)
)

// realtimeService adapts Echo requests to the realtime websocket reverse proxy.
// It stays a thin transport layer: validate, authorize, enforce budget, resolve
// the upstream target (credential injection lives in the provider), then relay
// frames verbatim. The realtime event schema is the wire format, so no request
// or response translation happens here.
type realtimeService struct {
	provider        core.RoutableProvider
	modelResolver   RequestModelResolver
	modelAuthorizer RequestModelAuthorizer
	budgetChecker   BudgetChecker
	rateLimiter     RateLimiter
	usageLogger     usage.LoggerInterface
	pricingResolver usage.PricingResolver
	calls           *realtime.CallRegistry
	httpClient      *http.Client
	enabled         bool
}

// realtimeRoute carries the resolved routing identity for one session, used to
// label its usage entries and lifecycle logs the same way audio does. endpoint,
// when set, overrides the usage entry endpoint so WebRTC calls are
// distinguishable from websocket sessions. selector is the fully resolved model,
// used to route upstream on the concrete model rather than the requested alias.
type realtimeRoute struct {
	selector     core.ModelSelector
	model        string
	providerType string
	providerName string
	requestID    string
	userPath     string
	sessionID    string
	labels       []string
	endpoint     string
}

// Realtime handles GET /v1/realtime, routing by the model query parameter. The
// intent query parameter selects a specialized session type on providers that
// offer one.
func (s *realtimeService) Realtime(c *echo.Context) error {
	return s.handle(c, strings.TrimSpace(c.QueryParam("model")), strings.TrimSpace(c.QueryParam("provider")), strings.TrimSpace(c.QueryParam("intent")))
}

// RealtimeTranslations handles GET /v1/realtime/translations, the dedicated
// endpoint for speech translation sessions. It is the same session flow with the
// intent fixed by the route, mirroring the provider surface clients already
// target.
func (s *realtimeService) RealtimeTranslations(c *echo.Context) error {
	return s.handle(c, strings.TrimSpace(c.QueryParam("model")), strings.TrimSpace(c.QueryParam("provider")), core.RealtimeIntentTranslation)
}

// PassthroughRealtime handles a websocket upgrade on /p/{provider}/v1/realtime
// and /p/{provider}/v1/realtime/translations. It routes by the same model
// resolution as the typed routes, pinning the provider named in the path as the
// resolution hint.
func (s *realtimeService) PassthroughRealtime(c *echo.Context, providerType, intent string) error {
	if intent == "" {
		intent = strings.TrimSpace(c.QueryParam("intent"))
	}
	return s.handle(c, strings.TrimSpace(c.QueryParam("model")), providerType, intent)
}

// handle is the single realtime entry point: gate, resolve+authorize, dial, relay.
// A call_id query parameter attaches to an existing WebRTC/SIP call as a sideband
// channel; the route is recalled from the call registry, or taken from explicit
// model/provider parameters when the call was created elsewhere.
func (s *realtimeService) handle(c *echo.Context, model, providerHint, intent string) error {
	if !s.enabled {
		return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "realtime sessions are disabled", nil))
	}
	router, ok := s.provider.(core.RealtimeRouter)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("realtime is not supported by the current provider router", nil))
	}
	callID := strings.TrimSpace(c.QueryParam("call_id"))
	if model == "" && callID == "" {
		return handleError(c, core.NewInvalidRequestError("model query parameter is required", nil))
	}
	registered := false
	if callID != "" {
		callRoute, ok := s.calls.Lookup(callID)
		switch {
		case ok:
			registered = true
			// The registry is authoritative for gateway-created calls: the attach
			// dials by call_id alone, so a client-supplied model or provider that
			// disagrees with the registered route must not steer model-access or
			// rate-limit checks toward a model the call is not running.
			model = callRoute.Model
			if callRoute.Provider != "" {
				providerHint = callRoute.Provider
			}
		case model == "":
			return handleError(c, core.NewNotFoundError("unknown call_id; pass model (and provider) query parameters to attach to a call created elsewhere"))
		}
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
	// Route on the resolved selector: an alias never reaches the provider lookup.
	// The intent asks the provider for a transcription or translation session;
	// the model still resolves routing, access, and usage attribution above.
	target, err := router.RealtimeTarget(ctx, &core.RealtimeRequest{
		Model:    route.selector.Model,
		Provider: route.selector.Provider,
		CallID:   callID,
		Intent:   intent,
	})
	if err != nil {
		return handleError(c, err)
	}
	tap := s.usageTap(ctx, route)
	if registered {
		// The gateway created this call and its sideband observer already records
		// usage; tapping the attach session too would double-count every response.
		tap = nil
	}
	hooks := realtime.Hooks{OnServerFrame: tap}
	// A provider that leaves the model out of the upstream URL (OpenAI
	// transcription sessions) asks the gateway to pin the client's in-session
	// model selection to the routed model the caller was authorized for.
	if model := strings.TrimSpace(target.PinSessionModel); model != "" {
		hooks.MapClientFrame = pinTranscriptionModel(model)
	}
	// A session that reports no usage of its own (OpenAI translation sessions)
	// is billed from the audio the gateway relays into it.
	var meter *usage.RealtimeInputAudioMeter
	if target.MeterInputAudio && tap != nil {
		meter = &usage.RealtimeInputAudioMeter{}
		hooks.OnClientFrame = meter.Observe
	}

	err = s.proxy(c, ctx, target, route, hooks)
	s.recordMeteredUsage(route, meter)
	return err
}

// recordMeteredUsage writes the single usage entry for a metered session once it
// ends. Metered sessions carry no usage events, so this is their only accounting;
// a session that relayed no audio produced no billable duration and is skipped.
func (s *realtimeService) recordMeteredUsage(route realtimeRoute, meter *usage.RealtimeInputAudioMeter) {
	if meter == nil {
		return
	}
	seconds := meter.Seconds()
	if seconds <= 0 {
		return
	}
	entry := usage.NewRealtimeDurationEntry(seconds, route.requestID, route.model, route.providerType, s.pricingFor(route))
	s.writeUsage(entry, route)
}

// prepare resolves and authorizes the model, enforces budget, and stamps the
// request id. It mirrors audioService.prepare so realtime sessions are gated by
// the same model-access and budget rules as the other model endpoints.
func (s *realtimeService) prepare(c *echo.Context, model, providerHint string) (context.Context, realtimeRoute, error) {
	selector, err := resolveServiceModel(c.Request().Context(), s.provider, s.modelResolver, model, providerHint)
	if err != nil {
		return nil, realtimeRoute{}, err
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
		selector:     selector,
		model:        selector.Model,
		providerType: s.provider.GetProviderType(qualified),
		providerName: selector.Provider,
		requestID:    requestID,
		userPath:     core.UserPathFromContext(ctx),
		sessionID:    core.SessionIDFromContext(ctx),
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
func (s *realtimeService) proxy(c *echo.Context, ctx context.Context, target *core.RealtimeTarget, route realtimeRoute, hooks realtime.Hooks) error {
	if target == nil || strings.TrimSpace(target.URL) == "" {
		return handleError(c, core.NewProviderError(route.providerType, http.StatusBadGateway, "provider returned no realtime target", nil))
	}

	t := realtime.Target{
		URL:          target.URL,
		Headers:      realtimeUpstreamHeaders(ctx, c.Request().Header, target.Headers),
		Subprotocols: target.Subprotocols,
	}

	slog.Info("realtime session opened", "request_id", route.requestID, "model", route.model, "provider", route.providerType)
	err := realtime.Proxy(c.Response(), c.Request(), t, hooks)

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
// usage event ("response.done", or the transcription completed event for
// transcription sessions), or nil when usage tracking is off (so the proxy skips
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
		isResponseDone := bytes.Contains(frame, responseDoneMarker)
		if !isResponseDone && !bytes.Contains(frame, transcriptionUsageMarker) {
			return
		}
		pricing := s.pricingFor(route)
		var entry *usage.UsageEntry
		if isResponseDone {
			entry = usage.ExtractFromRealtimeResponseDone(frame, route.requestID, route.model, route.providerType, pricing)
		} else {
			entry = usage.ExtractFromRealtimeTranscriptionCompleted(frame, route.requestID, route.model, route.providerType, pricing)
		}
		s.writeUsage(entry, route)
	}
}

// pricingFor resolves the model pricing used to cost a session's usage entries.
func (s *realtimeService) pricingFor(route realtimeRoute) *core.ModelPricing {
	if s.pricingResolver == nil {
		return nil
	}
	provider := route.providerName
	if provider == "" {
		provider = route.providerType
	}
	return s.pricingResolver.ResolvePricing(route.model, provider)
}

// writeUsage labels a usage entry with the session's routing identity and hands
// it to the logger. It ignores a nil entry so callers can pass the result of an
// extraction that found nothing billable.
func (s *realtimeService) writeUsage(entry *usage.UsageEntry, route realtimeRoute) {
	if entry == nil || s.usageLogger == nil {
		return
	}
	if route.endpoint != "" {
		entry.Endpoint = route.endpoint
	}
	entry.ProviderName = strings.TrimSpace(route.providerName)
	entry.UserPath = route.userPath
	entry.SessionID = route.sessionID
	entry.Labels = route.labels
	s.usageLogger.Write(entry)
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
