package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/realtime"
)

// realtimeCallsPath labels usage entries and Location headers for WebRTC calls.
const realtimeCallsPath = "/v1/realtime/calls"

// RealtimeCalls handles POST /v1/realtime/calls: the OpenAI-compatible WebRTC
// SDP exchange. Like the websocket route it is a transport concern: the gateway
// resolves the model, injects provider credentials, forwards the SDP offer, and
// relays the SDP answer verbatim. The media itself flows peer-to-provider and
// never transits the gateway, so usage is recorded by a sideband observer
// attached to the created call.
func (s *realtimeService) RealtimeCalls(c *echo.Context) error {
	router, err := s.callRouter()
	if err != nil {
		return handleError(c, err)
	}

	offer, err := parseRealtimeCallOffer(c)
	if err != nil {
		return handleError(c, err)
	}

	providerHint := strings.TrimSpace(c.QueryParam("provider"))
	ctx, route, err := s.prepare(c, offer.model, providerHint)
	if err != nil {
		return handleError(c, err)
	}
	route.endpoint = realtimeCallsPath

	// Signaling is a plain HTTP exchange: the reservation is released when the
	// exchange finishes. Concurrent-scope rules cannot span the call lifetime
	// because the media never transits the gateway.
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()

	// Route on the resolved selector: an alias never reaches the provider lookup.
	target, err := router.RealtimeCallTarget(ctx, &core.RealtimeRequest{Model: route.selector.Model, Provider: route.selector.Provider})
	if err != nil {
		return handleError(c, err)
	}

	body, contentType, query, err := offer.render(route.model)
	if err != nil {
		return handleError(c, err)
	}
	resp, err := s.forwardRealtimeHTTP(ctx, target, body, contentType, query, route)
	if err != nil {
		return handleError(c, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return handleError(c, realtimeUpstreamError(route.providerType, resp))
	}

	if callID := realtimeCallIDFromLocation(resp.Header.Get("Location")); callID != "" {
		callProvider := route.providerName
		if callProvider == "" {
			callProvider = route.providerType
		}
		s.calls.Register(callID, realtime.CallRoute{Model: route.model, Provider: callProvider})
		c.Response().Header().Set("Location", realtimeCallsPath+"/"+callID)
		slog.Info("realtime call created", "request_id", route.requestID, "model", route.model, "provider", route.providerType, "call_id", callID)
		s.observeCall(ctx, route, callID)
	}

	return relayRealtimeHTTPResponse(c, resp, "application/sdp")
}

// RealtimeClientSecrets handles POST /v1/realtime/client_secrets: minting
// ephemeral realtime credentials for browser clients. The session model routes
// the request and is rewritten to the resolved provider model; everything else
// is relayed verbatim. The minted secret authenticates the client directly
// against the provider, so usage of sessions opened with it is not observed by
// the gateway.
func (s *realtimeService) RealtimeClientSecrets(c *echo.Context) error {
	router, err := s.callRouter()
	if err != nil {
		return handleError(c, err)
	}

	var payload map[string]any
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid JSON body: "+err.Error(), err))
	}
	model, setModel := realtimeSessionModel(payload)
	if model == "" {
		return handleError(c, core.NewInvalidRequestError("session.model is required to mint a realtime client secret", nil))
	}

	providerHint := strings.TrimSpace(c.QueryParam("provider"))
	ctx, route, err := s.prepare(c, model, providerHint)
	if err != nil {
		return handleError(c, err)
	}
	route.endpoint = "/v1/realtime/client_secrets"

	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()

	// Route on the resolved selector: an alias never reaches the provider lookup.
	target, err := router.RealtimeClientSecretTarget(ctx, &core.RealtimeRequest{Model: route.selector.Model, Provider: route.selector.Provider})
	if err != nil {
		return handleError(c, err)
	}

	setModel(route.model)
	body, err := json.Marshal(payload)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("failed to encode client secret request: "+err.Error(), err))
	}

	resp, err := s.forwardRealtimeHTTP(ctx, target, bytes.NewReader(body), "application/json", nil, route)
	if err != nil {
		return handleError(c, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return handleError(c, realtimeUpstreamError(route.providerType, resp))
	}
	return relayRealtimeHTTPResponse(c, resp, "application/json")
}

// callRouter gates the realtime HTTP signaling routes and narrows the provider
// router to the WebRTC call capability.
func (s *realtimeService) callRouter() (core.RealtimeCallRouter, error) {
	if !s.enabled {
		return nil, core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "realtime sessions are disabled", nil)
	}
	router, ok := s.provider.(core.RealtimeCallRouter)
	if !ok {
		return nil, core.NewInvalidRequestError("realtime calls are not supported by the current provider router", nil)
	}
	return router, nil
}

// forwardRealtimeHTTP posts the signaling body to the provider target with
// injected credentials. Client headers are never forwarded: typed realtime
// signaling is a translated route, and the wire format is fully described by
// the body and content type.
func (s *realtimeService) forwardRealtimeHTTP(ctx context.Context, target *core.RealtimeHTTPTarget, body io.Reader, contentType string, query url.Values, route realtimeRoute) (*http.Response, error) {
	if target == nil || strings.TrimSpace(target.URL) == "" {
		return nil, core.NewProviderError(route.providerType, http.StatusBadGateway, "provider returned no realtime signaling target", nil)
	}
	endpoint, err := url.Parse(target.URL)
	if err != nil {
		return nil, core.NewProviderError(route.providerType, http.StatusBadGateway, "provider returned an invalid realtime signaling target", err)
	}
	if len(query) > 0 {
		merged := endpoint.Query()
		maps.Copy(merged, query)
		endpoint.RawQuery = merged.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return nil, core.NewProviderError(route.providerType, http.StatusBadGateway, "failed to build realtime signaling request", err)
	}
	for key, values := range target.Headers {
		req.Header[http.CanonicalHeaderKey(key)] = values
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// The fallback keeps hand-constructed services (tests, embedded wiring)
	// working, but never with an unbounded client: signaling requests must
	// honor the configured HTTP timeouts.
	client := s.httpClient
	if client == nil {
		client = httpclient.NewDefaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, core.NewProviderError(route.providerType, http.StatusBadGateway, "failed to reach realtime signaling endpoint", err)
	}
	return resp, nil
}

// realtimeUpstreamError normalizes an upstream signaling failure into the
// gateway's OpenAI-compatible error shape, like the passthrough routes do.
func realtimeUpstreamError(providerType string, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewProviderError(providerType, http.StatusBadGateway, "failed to read realtime signaling error response", err)
	}
	return core.ParseProviderError(providerType, resp.StatusCode, body, nil)
}

// relayRealtimeHTTPResponse copies the upstream status and body to the client.
// The Location header is set by the caller after rewriting. The content type is
// pinned to the protocol-mandated value rather than echoed from upstream, and
// sniffing is disabled, so a misbehaving upstream cannot turn the relayed bytes
// into markup a browser would render.
func relayRealtimeHTTPResponse(c *echo.Context, resp *http.Response, contentType string) error {
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().WriteHeader(resp.StatusCode)
	_, err := io.Copy(c.Response(), resp.Body)
	return err
}

// realtimeCallIDFromLocation extracts the call id from the Location header of a
// call creation response (e.g. "/v1/realtime/calls/rtc_123" -> "rtc_123").
// Absolute URLs are handled so the upstream host never leaks to clients.
func realtimeCallIDFromLocation(location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return segments[len(segments)-1]
}

// observeCall attaches a sideband websocket to a freshly created WebRTC call and
// records usage per response.done event. It is best effort: WebRTC media and
// events flow peer-to-provider, so this observer is the gateway's only view of
// the call; if it cannot attach, the call proceeds untracked and a warning is
// logged. No-op when usage tracking is off.
func (s *realtimeService) observeCall(ctx context.Context, route realtimeRoute, callID string) {
	tap := s.usageTap(ctx, route)
	if tap == nil {
		return
	}
	router, ok := s.provider.(core.RealtimeRouter)
	if !ok {
		return
	}
	providerHint := route.providerName
	if providerHint == "" {
		providerHint = route.providerType
	}
	target, err := router.RealtimeTarget(ctx, &core.RealtimeRequest{Model: route.model, Provider: providerHint, CallID: callID})
	if err != nil || target == nil || strings.TrimSpace(target.URL) == "" {
		slog.Warn("realtime call observer target unavailable; usage will not be recorded",
			"request_id", route.requestID, "call_id", callID, "error", err)
		return
	}
	t := realtime.Target{URL: target.URL, Headers: target.Headers, Subprotocols: target.Subprotocols}

	go func() {
		// The observer outlives the signaling request: it runs for the call
		// lifetime, ended by the provider closing the sideband socket. The
		// registry TTL caps runaway sessions.
		obsCtx, cancel := context.WithTimeout(context.Background(), realtime.DefaultCallTTL)
		defer cancel()
		err := realtime.Observe(obsCtx, t, tap)
		var de *realtime.DialError
		if errors.As(err, &de) {
			slog.Warn("realtime call observer failed to attach; usage will not be recorded",
				"request_id", route.requestID, "call_id", callID, "error", de.Err)
			return
		}
		if err != nil {
			slog.Warn("realtime call observer closed with error", "request_id", route.requestID, "call_id", callID, "error", err)
			return
		}
		slog.Info("realtime call ended", "request_id", route.requestID, "call_id", callID)
	}()
}

// realtimeSessionModel extracts the routing model from a realtime session
// config and returns a setter that rewrites the same field to the resolved
// provider model. Realtime sessions carry it at session.model; transcription
// sessions nest it under session.audio.input.transcription.model (Postel:
// accept both).
func realtimeSessionModel(payload map[string]any) (string, func(string)) {
	session, ok := payload["session"].(map[string]any)
	if !ok {
		return "", nil
	}
	if model, ok := session["model"].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model), func(m string) { session["model"] = m }
	}
	audio, _ := session["audio"].(map[string]any)
	input, _ := audio["input"].(map[string]any)
	if transcription, _ := input["transcription"].(map[string]any); transcription != nil {
		if model, ok := transcription["model"].(string); ok && strings.TrimSpace(model) != "" {
			return strings.TrimSpace(model), func(m string) { transcription["model"] = m }
		}
	}
	return "", nil
}

// realtimeCallOffer is a parsed POST /v1/realtime/calls request. The GA API
// accepts two shapes: a raw application/sdp offer with the model in the query
// string, and a multipart form with "sdp" and "session" (JSON) fields.
type realtimeCallOffer struct {
	model       string
	fromQuery   bool
	sdp         []byte             // raw body for the application/sdp shape
	contentType string             // original content type for the raw shape
	form        []realtimeFormPart // parts in order for the multipart shape
	session     map[string]any     // decoded session JSON, nil when absent
	setModel    func(string)       // rewrites the session model in place
}

type realtimeFormPart struct {
	header  textproto.MIMEHeader
	name    string
	content []byte
}

// parseRealtimeCallOffer reads and classifies the request body, resolving the
// routing model from the query parameter or the session form field.
func parseRealtimeCallOffer(c *echo.Context) (*realtimeCallOffer, error) {
	queryModel := strings.TrimSpace(c.QueryParam("model"))
	contentType := c.Request().Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)

	if !strings.HasPrefix(mediaType, "multipart/") {
		// Anything that is not a multipart form is treated as a raw SDP offer
		// (Postel: clients send application/sdp, but the body is opaque anyway).
		if queryModel == "" {
			return nil, core.NewInvalidRequestError("model query parameter is required for SDP offers", nil)
		}
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to read SDP offer: "+err.Error(), err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, core.NewInvalidRequestError("SDP offer body is required", nil)
		}
		if contentType == "" {
			contentType = "application/sdp"
		}
		return &realtimeCallOffer{model: queryModel, fromQuery: true, sdp: body, contentType: contentType}, nil
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil, core.NewInvalidRequestError("multipart body is missing a boundary", nil)
	}
	offer := &realtimeCallOffer{model: queryModel, fromQuery: queryModel != ""}
	reader := multipart.NewReader(c.Request().Body, boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid multipart body: "+err.Error(), err)
		}
		content, err := io.ReadAll(part)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to read multipart body: "+err.Error(), err)
		}
		offer.form = append(offer.form, realtimeFormPart{header: part.Header, name: part.FormName(), content: content})
		if part.FormName() == "session" {
			var session map[string]any
			if err := json.Unmarshal(content, &session); err != nil {
				return nil, core.NewInvalidRequestError("invalid session JSON in multipart body: "+err.Error(), err)
			}
			model, setModel := realtimeSessionModel(map[string]any{"session": session})
			offer.session = session
			offer.setModel = setModel
			if offer.model == "" {
				offer.model = model
			}
		}
	}
	if offer.model == "" {
		return nil, core.NewInvalidRequestError("model is required: pass a model query parameter or a session.model form field", nil)
	}
	return offer, nil
}

// render produces the upstream body, content type, and query parameters with
// the resolved provider model in place of the requested one, so aliases and
// virtual models stay gateway-side. Multipart bodies are rebuilt with a fresh
// boundary; raw SDP offers forward untouched with the model in the query.
func (o *realtimeCallOffer) render(resolvedModel string) (io.Reader, string, url.Values, error) {
	if o.form == nil {
		return bytes.NewReader(o.sdp), o.contentType, url.Values{"model": []string{resolvedModel}}, nil
	}

	if o.setModel != nil {
		o.setModel(resolvedModel)
	} else if o.session != nil {
		o.session["model"] = resolvedModel
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, part := range o.form {
		content := part.content
		if part.name == "session" && o.session != nil {
			encoded, err := json.Marshal(o.session)
			if err != nil {
				return nil, "", nil, core.NewInvalidRequestError("failed to encode session form field: "+err.Error(), err)
			}
			content = encoded
		}
		w, err := writer.CreatePart(part.header)
		if err != nil {
			return nil, "", nil, core.NewInvalidRequestError("failed to rebuild multipart body: "+err.Error(), err)
		}
		if _, err := w.Write(content); err != nil {
			return nil, "", nil, core.NewInvalidRequestError("failed to rebuild multipart body: "+err.Error(), err)
		}
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, "", nil, core.NewInvalidRequestError("failed to rebuild multipart body: "+err.Error(), err)
	}

	var query url.Values
	if o.fromQuery {
		query = url.Values{"model": []string{resolvedModel}}
	}
	return &buf, contentType, query, nil
}
