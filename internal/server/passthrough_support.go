package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
)

var defaultEnabledPassthroughProviders = []string{"openai", "anthropic", "openrouter", "kilo", "zai", "sglang", "vllm", "llamacpp", "llmd", "deepseek", "hetzner"}

const llmdDroppedReasonHeader = "X-Llm-D-Request-Dropped-Reason"

func (h *Handler) setEnabledPassthroughProviders(providerTypes []string) {
	h.enabledPassthroughProviders = normalizeEnabledPassthroughProviders(providerTypes)
}

func isEnabledPassthroughProvider(providerType string, enabledPassthroughProviders map[string]struct{}) bool {
	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return false
	}
	_, ok := enabledPassthroughProviders[providerType]
	return ok
}

func normalizeEnabledPassthroughProviders(providerTypes []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(providerTypes))
	for _, providerType := range providerTypes {
		providerType = strings.TrimSpace(providerType)
		if providerType == "" {
			continue
		}
		allowed[providerType] = struct{}{}
	}
	return allowed
}

func (s *passthroughService) enabledPassthroughProviderNames() []string {
	providers := make([]string, 0, len(s.enabledPassthroughProviders))
	for providerType := range s.enabledPassthroughProviders {
		providers = append(providers, providerType)
	}
	sort.Strings(providers)
	return providers
}

func (s *passthroughService) unsupportedPassthroughProviderError(providerType string) error {
	providers := s.enabledPassthroughProviderNames()
	if len(providers) == 0 {
		return core.NewInvalidRequestError("provider passthrough is not enabled for any providers", nil)
	}
	return core.NewInvalidRequestError(
		fmt.Sprintf("provider passthrough for %q is not enabled; currently enabled providers: %s", strings.TrimSpace(providerType), strings.Join(providers, ", ")),
		nil,
	)
}

func normalizePassthroughEndpoint(endpoint string, enabled bool) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "v1":
		if !enabled {
			return "", core.NewInvalidRequestError("provider passthrough v1 alias is disabled; use /p/{provider}/... without the v1 prefix", nil)
		}
		return "", nil
	case strings.HasPrefix(endpoint, "v1/"):
		if !enabled {
			return "", core.NewInvalidRequestError("provider passthrough v1 alias is disabled; use /p/{provider}/... without the v1 prefix", nil)
		}
		return strings.TrimPrefix(endpoint, "v1/"), nil
	default:
		return endpoint, nil
	}
}

func buildPassthroughHeaders(ctx context.Context, src http.Header) http.Header {
	connectionHeaders := passthroughConnectionHeaders(src)
	userPathHeaderName := http.CanonicalHeaderKey(core.UserPathHeaderNameFromContext(ctx))
	taggingStrip := core.TaggingStripHeadersFromContext(ctx)
	dst := make(http.Header)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if skipPassthroughRequestHeader(canonicalKey, userPathHeaderName) || len(values) == 0 {
			continue
		}
		if _, doNotPass := taggingStrip[canonicalKey]; doNotPass {
			continue
		}
		if _, hopByHop := connectionHeaders[canonicalKey]; hopByHop {
			continue
		}
		clonedValues := make([]string, len(values))
		copy(clonedValues, values)
		dst[canonicalKey] = clonedValues
	}
	requestID := strings.TrimSpace(src.Get(core.RequestIDHeader))
	if requestID == "" {
		requestID = strings.TrimSpace(core.GetRequestID(ctx))
	}
	if requestID != "" && strings.TrimSpace(dst.Get(core.RequestIDHeader)) == "" {
		dst.Set(core.RequestIDHeader, requestID)
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func skipPassthroughHeader(key string) bool {
	canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
	switch canonicalKey {
	case "Authorization", "X-Api-Key", "Host", "Content-Length", "Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Cookie", "Forwarded", "Set-Cookie":
		return true
	default:
		return strings.HasPrefix(canonicalKey, "X-Forwarded-")
	}
}

func skipPassthroughRequestHeader(key string, userPathHeader ...string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	// A forwarded Accept-Encoding makes Go's transport return the upstream
	// body still compressed, which blinds the audit and usage SSE observers.
	// Dropping it lets the transport negotiate gzip itself and hand back
	// decoded bytes; the client then receives an uncompressed response with
	// the Content-Encoding header removed by the transport.
	if strings.EqualFold(key, "Accept-Encoding") {
		return true
	}
	if strings.EqualFold(key, core.UserPathHeader) {
		return true
	}
	for _, headerName := range userPathHeader {
		if strings.EqualFold(key, headerName) {
			return true
		}
	}
	return skipPassthroughHeader(key)
}

func passthroughConnectionHeaders(headers http.Header) map[string]struct{} {
	var tokens map[string]struct{}
	for key, values := range headers {
		if http.CanonicalHeaderKey(strings.TrimSpace(key)) != "Connection" {
			continue
		}
		for _, value := range values {
			for token := range strings.SplitSeq(value, ",") {
				canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(token))
				if canonicalKey == "" {
					continue
				}
				if tokens == nil {
					tokens = make(map[string]struct{})
				}
				tokens[canonicalKey] = struct{}{}
			}
		}
	}
	return tokens
}

func copyPassthroughResponseHeaders(dst, src http.Header) {
	connectionHeaders := passthroughConnectionHeaders(src)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if skipPassthroughHeader(canonicalKey) || len(values) == 0 {
			continue
		}
		if _, hopByHop := connectionHeaders[canonicalKey]; hopByHop {
			continue
		}
		dst.Del(canonicalKey)
		for _, value := range values {
			dst.Add(canonicalKey, value)
		}
	}
}

func isSSEContentType(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

func passthroughStreamAuditPath(requestPath, providerType, endpoint string) string {
	normalized := "/" + strings.TrimLeft(strings.SplitN(endpoint, "?", 2)[0], "/")
	switch providerType {
	case "openai":
		switch normalized {
		case "/chat/completions":
			return "/v1/chat/completions"
		case "/responses":
			return "/v1/responses"
		}
	case "anthropic":
		switch normalized {
		case "/messages":
			return "/v1/messages"
		}
	}
	return requestPath
}

func passthroughAuditPath(c *echo.Context, providerType, endpoint string, info *core.PassthroughRouteInfo) string {
	if info != nil {
		if auditPath := strings.TrimSpace(info.AuditPath); auditPath != "" {
			return auditPath
		}
	}
	if c != nil {
		if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil && workflow.Passthrough != nil {
			if auditPath := strings.TrimSpace(workflow.Passthrough.AuditPath); auditPath != "" {
				return auditPath
			}
		}
		if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil {
			if info := env.CachedPassthroughRouteInfo(); info != nil {
				if auditPath := strings.TrimSpace(info.AuditPath); auditPath != "" {
					return auditPath
				}
			}
		}
		if requestPath := strings.TrimSpace(c.Request().URL.Path); requestPath != "" {
			return passthroughStreamAuditPath(requestPath, providerType, endpoint)
		}
	}
	return passthroughStreamAuditPath("", providerType, endpoint)
}

func (s *passthroughService) proxyPassthroughResponse(c *echo.Context, providerType, providerName, endpoint string, info *core.PassthroughRouteInfo, resp *core.PassthroughResponse) error {
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, providerType, providerName, endpoint, info, resp)
}

// proxyPassthroughResponse relays a provider-native response (JSON or SSE) to
// the client, attaching audit and usage stream observers plus any
// extraObservers the caller supplies for SSE responses. It is shared by the
// /p/ passthrough surface and the /v1/messages native forwarding path.
func proxyPassthroughResponse(c *echo.Context, logger auditlog.LoggerInterface, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver, providerType, providerName, endpoint string, info *core.PassthroughRouteInfo, resp *core.PassthroughResponse, extraObservers ...streaming.Observer) error {
	if resp == nil || resp.Body == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty passthrough response", nil))
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		return relayPassthroughError(c, providerType, resp)
	}

	copyPassthroughResponseHeaders(c.Response().Header(), http.Header(resp.Headers))

	if isSSEContentType(resp.Headers) {
		auditlog.MarkEntryAsStreaming(c, true)
		auditlog.EnrichEntryWithStream(c, true)
		workflow := core.GetWorkflow(c.Request().Context())
		auditEnabled := logger != nil && logger.Config().Enabled && (workflow == nil || workflow.AuditEnabled())

		entry := auditlog.GetStreamEntryFromContext(c)
		if auditEnabled && entry != nil {
			auditlog.PopulateRequestData(entry, c.Request(), logger.Config())
		}
		streamEntry := auditlog.CreateStreamEntry(c.Request().Context(), entry)
		if streamEntry != nil {
			streamEntry.StatusCode = resp.StatusCode
		}
		if auditEnabled && streamEntry != nil && logger.Config().LogHeaders {
			auditlog.PopulateResponseHeaders(streamEntry, c.Response().Header())
		}

		requestID := requestIDFromContextOrHeader(c.Request())
		auditPath := passthroughAuditPath(c, providerType, endpoint, info)
		usagePath := auditPath
		if requestPath := strings.TrimSpace(c.Request().URL.Path); requestPath != "" {
			usagePath = requestPath
		}
		model := ""
		if info != nil {
			model = strings.TrimSpace(info.Model)
		}
		model = resolvedModelFromWorkflow(workflow, model)

		observers := make([]streaming.Observer, 0, 2+len(extraObservers))
		if auditEnabled && streamEntry != nil {
			if observer := auditlog.NewStreamLogObserver(logger, streamEntry, auditPath); observer != nil {
				observers = append(observers, observer)
			}
		}
		if observer := passthroughUsageObserver(c, usageLogger, pricingResolver, workflow, model, providerType, providerName, requestID, usagePath); observer != nil {
			observers = append(observers, observer)
		}
		observers = append(observers, extraObservers...)
		wrappedStream := streaming.NewObservedSSEStream(resp.Body, observers...)
		if len(observers) > 0 {
			defer func() {
				_ = wrappedStream.Close()
			}()
		}

		c.Response().WriteHeader(resp.StatusCode)
		if err := flushStream(c.Response(), wrappedStream); err != nil {
			recordStreamingError(streamEntry, model, providerType, c.Request().URL.Path, requestID, c.Request().Context(), err)
			return err
		}
		return nil
	}

	// Non-streaming JSON responses carry usage inside the response object
	// itself (top-level "usage" for Anthropic messages and OpenAI chat
	// completions). Tee the relay into a bounded buffer and feed the complete
	// body to the same observers as a single synthetic event, so usage
	// accounting and response feedback match the SSE behavior. The audit
	// stream observer is deliberately absent: non-streaming responses are
	// audited by the regular audit middleware.
	var observers []streaming.Observer
	if isObservablePassthroughStatus(resp.StatusCode) {
		observers = passthroughJSONResponseObservers(c, usageLogger, pricingResolver, providerType, providerName, endpoint, info, extraObservers)
	}
	if len(observers) == 0 || !isJSONContentType(resp.Headers) {
		c.Response().WriteHeader(resp.StatusCode)
		if _, err := io.Copy(c.Response(), resp.Body); err != nil {
			return err
		}
		if f, ok := c.Response().(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}

	capture := newCappedCaptureBuffer(maxObservedJSONResponseBytes)
	c.Response().WriteHeader(resp.StatusCode)
	if _, err := io.Copy(c.Response(), io.TeeReader(resp.Body, capture)); err != nil {
		// The client received an incomplete body; do not account for it.
		return err
	}
	if f, ok := c.Response().(http.Flusher); ok {
		f.Flush()
	}
	if body, ok := capture.Captured(); ok {
		notifyObserversWithJSONBody(body, observers)
	}
	return nil
}

// maxRelayedPassthroughErrorBytes caps the provider-native error body relayed
// verbatim. Provider error payloads are small; anything larger is answered
// with the gateway's own envelope rather than buffered.
const maxRelayedPassthroughErrorBytes = 1 << 20

// relayPassthroughError answers a non-2xx passthrough response with the
// provider's own error body, status and content type. That is the documented
// passthrough contract: provider SDKs parse their native error envelope
// (Anthropic's {"type":"error",...} with its request_id) and lose their typed
// errors when the gateway rewrites it into the OpenAI envelope.
//
// Only the upstream's own failures reach here. Gateway-generated errors —
// authentication, rate limits, budgets, unknown or disabled providers,
// transport failures — are rendered by handleError before or instead of this
// relay, so they keep the gateway's shape. Nothing but the body, its media
// type and the small set of headers the gateway already relays on errors
// crosses back, so upstream credentials, cookies and base URLs cannot leak.
//
// Bodies that are not a JSON payload within the cap (an intermediary's HTML
// error page, for example) fall back to the gateway envelope, so a client
// always receives a parseable error.
func relayPassthroughError(c *echo.Context, providerType string, resp *core.PassthroughResponse) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRelayedPassthroughErrorBytes+1))
	if err != nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "failed to read provider passthrough error response", err))
	}
	gatewayErr := core.ParseProviderError(providerType, resp.StatusCode, body, nil)
	headers := passthroughErrorResponseHeaders(providerType, resp.StatusCode, http.Header(resp.Headers))
	if !isRelayablePassthroughErrorBody(body, resp.Headers) {
		if len(headers) == 0 {
			return handleError(c, gatewayErr)
		}
		return handleError(c, &gatewayErrorWithResponseHeaders{GatewayError: gatewayErr, headers: headers})
	}

	// The client receives the provider body; logs and the audit row still carry
	// the classified gateway error, exactly as handleError would record it.
	logHandledError(c, gatewayErr)
	enrichAuditEntryWithProviderAttempts(c)
	auditlog.EnrichEntryWithGatewayError(c, gatewayErr)

	for key, values := range headers {
		for _, value := range values {
			c.Response().Header().Add(key, value)
		}
	}
	c.Response().Header().Set("Content-Type", passthroughErrorContentType(resp.Headers))
	c.Response().WriteHeader(resp.StatusCode)
	if _, err := c.Response().Write(body); err != nil {
		return err
	}
	if f, ok := c.Response().(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// isRelayablePassthroughErrorBody reports whether an upstream error body can be
// relayed to the client untouched: a non-empty, valid JSON document served
// with a JSON content type and small enough to buffer.
func isRelayablePassthroughErrorBody(body []byte, headers map[string][]string) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || len(body) > maxRelayedPassthroughErrorBytes {
		return false
	}
	return isJSONContentType(headers) && json.Valid(trimmed)
}

// passthroughErrorContentType returns the upstream JSON media type to relay
// with an error body, defaulting to application/json.
func passthroughErrorContentType(headers map[string][]string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" && !strings.ContainsAny(value, "\r\n") {
				return value
			}
		}
	}
	return "application/json"
}

// maxObservedJSONResponseBytes caps how much of a non-streaming JSON response
// is buffered for usage extraction. Inference responses are bounded by
// max_tokens and stay far below this; anything larger (e.g. a passthrough
// file download with a JSON content type) skips observation rather than
// holding the body in memory.
const maxObservedJSONResponseBytes = 8 << 20

// cappedCaptureBuffer records writes up to a fixed cap. Once the cap is
// exceeded the capture is abandoned (Captured reports false) while writes
// keep succeeding, so the client relay is never affected.
type cappedCaptureBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func newCappedCaptureBuffer(maxBytes int) *cappedCaptureBuffer {
	return &cappedCaptureBuffer{max: maxBytes}
}

func (b *cappedCaptureBuffer) Write(p []byte) (int, error) {
	if !b.overflow {
		if b.buf.Len()+len(p) > b.max {
			b.overflow = true
			b.buf.Reset()
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedCaptureBuffer) Captured() ([]byte, bool) {
	if b.overflow || b.buf.Len() == 0 {
		return nil, false
	}
	return b.buf.Bytes(), true
}

// passthroughUsageObserver builds the stream usage observer for a passthrough
// response when usage logging is enabled for the workflow, or nil.
func passthroughUsageObserver(c *echo.Context, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver, workflow *core.Workflow, model, providerType, providerName, requestID, usagePath string) *usage.StreamUsageObserver {
	if usageLogger == nil || !usageLogger.Config().Enabled || (workflow != nil && !workflow.UsageEnabled()) {
		return nil
	}
	observer := usage.NewStreamUsageObserver(usageLogger, model, providerType, requestID, usagePath, pricingResolver, core.UserPathFromContext(c.Request().Context()))
	if observer == nil {
		return nil
	}
	observer.SetProviderName(providerName)
	observer.SetSessionID(core.SessionIDFromContext(c.Request().Context()))
	observer.SetLabels(core.RequestLabelsFromContext(c.Request().Context()))
	observer.SetRewriteTokensSaved(core.RewriteTokensSavedFromContext(c.Request().Context()))
	return observer
}

// passthroughJSONResponseObservers assembles the observers interested in a
// completed non-streaming JSON passthrough response: the usage accounting
// observer plus any caller-supplied extras (response feedback).
func passthroughJSONResponseObservers(c *echo.Context, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver, providerType, providerName, endpoint string, info *core.PassthroughRouteInfo, extraObservers []streaming.Observer) []streaming.Observer {
	workflow := core.GetWorkflow(c.Request().Context())
	requestID := requestIDFromContextOrHeader(c.Request())
	usagePath := passthroughAuditPath(c, providerType, endpoint, info)
	if requestPath := strings.TrimSpace(c.Request().URL.Path); requestPath != "" {
		usagePath = requestPath
	}
	model := ""
	if info != nil {
		model = strings.TrimSpace(info.Model)
	}
	model = resolvedModelFromWorkflow(workflow, model)

	observers := make([]streaming.Observer, 0, 1+len(extraObservers))
	if observer := passthroughUsageObserver(c, usageLogger, pricingResolver, workflow, model, providerType, providerName, requestID, usagePath); observer != nil {
		observers = append(observers, observer)
	}
	return append(observers, extraObservers...)
}

// notifyObserversWithJSONBody replays a complete JSON response body to stream
// observers as one synthetic event followed by close, mirroring how the same
// payload would reach them as the final event of an SSE stream. Bodies that
// do not decode to a JSON object still close the observers out, so response
// feedback fires exactly once per response.
func notifyObserversWithJSONBody(body []byte, observers []streaming.Observer) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil && payload != nil {
		for _, observer := range observers {
			if filter, ok := observer.(streaming.EventFilter); ok && !filter.WantsJSONEvent(body) {
				continue
			}
			observer.OnJSONEvent(payload)
		}
	}
	for _, observer := range observers {
		observer.OnStreamClose()
	}
}

// isObservablePassthroughStatus reports whether a passthrough response status
// can carry a complete, accountable response body: any success status except
// 206 Partial Content, whose body is by definition incomplete.
func isObservablePassthroughStatus(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices && status != http.StatusPartialContent
}

func isJSONContentType(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			mediaType, _, err := mime.ParseMediaType(value)
			if err != nil {
				continue
			}
			mediaType = strings.ToLower(mediaType)
			if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
				return true
			}
		}
	}
	return false
}

func passthroughErrorResponseHeaders(providerType string, statusCode int, src http.Header) http.Header {
	if providerType != "llmd" || statusCode != http.StatusTooManyRequests {
		return nil
	}
	reason := strings.TrimSpace(src.Get(llmdDroppedReasonHeader))
	if reason == "" || strings.ContainsAny(reason, "\r\n") {
		return nil
	}
	return http.Header{llmdDroppedReasonHeader: []string{reason}}
}
