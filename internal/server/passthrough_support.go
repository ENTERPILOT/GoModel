package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

var defaultEnabledPassthroughProviders = []string{"openai", "anthropic"}

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
	dst := make(http.Header)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if skipPassthroughRequestHeader(canonicalKey) || len(values) == 0 {
			continue
		}
		if _, hopByHop := connectionHeaders[canonicalKey]; hopByHop {
			continue
		}
		clonedValues := make([]string, len(values))
		copy(clonedValues, values)
		dst[canonicalKey] = clonedValues
	}
	requestID := strings.TrimSpace(src.Get("X-Request-ID"))
	if requestID == "" {
		requestID = strings.TrimSpace(core.GetRequestID(ctx))
	}
	if requestID != "" && strings.TrimSpace(dst.Get("X-Request-ID")) == "" {
		dst.Set("X-Request-ID", requestID)
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

func skipPassthroughRequestHeader(key string) bool {
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
		if plan := core.GetExecutionPlan(c.Request().Context()); plan != nil && plan.Passthrough != nil {
			if auditPath := strings.TrimSpace(plan.Passthrough.AuditPath); auditPath != "" {
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

func (s *passthroughService) proxyPassthroughResponse(c *echo.Context, providerType, endpoint string, info *core.PassthroughRouteInfo, resp *core.PassthroughResponse) error {
	if resp == nil || resp.Body == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty passthrough response", nil))
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "failed to read provider passthrough error response", err))
		}
		return handleError(c, core.ParseProviderError(providerType, resp.StatusCode, body, nil))
	}

	copyPassthroughResponseHeaders(c.Response().Header(), http.Header(resp.Headers))

	if isSSEContentType(resp.Headers) {
		auditlog.MarkEntryAsStreaming(c, true)
		auditlog.EnrichEntryWithStream(c, true)
		plan := core.GetExecutionPlan(c.Request().Context())
		auditEnabled := s.logger != nil && s.logger.Config().Enabled && (plan == nil || plan.AuditEnabled())

		entry := auditlog.GetStreamEntryFromContext(c)
		if auditEnabled && entry != nil {
			auditlog.PopulateRequestData(entry, c.Request(), s.logger.Config())
		}
		streamEntry := auditlog.CreateStreamEntry(entry)
		if streamEntry != nil {
			streamEntry.StatusCode = resp.StatusCode
		}
		if auditEnabled && streamEntry != nil && s.logger.Config().LogHeaders {
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
		model = resolvedModelFromPlan(plan, model)

		observers := make([]streaming.Observer, 0, 2)
		if auditEnabled && streamEntry != nil {
			if observer := auditlog.NewStreamLogObserver(s.logger, streamEntry, auditPath); observer != nil {
				observers = append(observers, observer)
			}
		}
		if s.usageLogger != nil && s.usageLogger.Config().Enabled && (plan == nil || plan.UsageEnabled()) {
			if observer := usage.NewStreamUsageObserver(s.usageLogger, model, providerType, requestID, usagePath, s.pricingResolver, core.UserPathFromContext(c.Request().Context())); observer != nil {
				observers = append(observers, observer)
			}
		}
		wrappedStream := streaming.NewObservedSSEStream(resp.Body, observers...)
		if len(observers) > 0 {
			defer func() {
				_ = wrappedStream.Close()
			}()
		}

		c.Response().WriteHeader(resp.StatusCode)
		if err := flushStream(c.Response(), wrappedStream); err != nil {
			recordStreamingError(streamEntry, model, providerType, c.Request().URL.Path, requestID, err)
			return err
		}
		return nil
	}

	c.Response().WriteHeader(resp.StatusCode)
	if _, err := io.Copy(c.Response(), resp.Body); err != nil {
		return err
	}
	return nil
}
