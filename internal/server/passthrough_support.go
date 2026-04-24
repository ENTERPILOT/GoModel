package server

import (
	"context"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

// buildPassthroughHeaders constructs the outbound header set for an upstream
// passthrough request. It copies sanitized inbound headers, strips hop-by-hop
// and gateway-internal headers, and injects the X-GoModel-Request-ID so the
// upstream response can be correlated back to the originating request.
func buildPassthroughHeaders(_ context.Context, src http.Header, goModelRequestID string) http.Header {
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
	if id := strings.TrimSpace(goModelRequestID); id != "" {
		dst.Set(core.RequestIDHeader, id)
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
	canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
	switch canonicalKey {
	case http.CanonicalHeaderKey(core.RequestIDHeader),
		http.CanonicalHeaderKey(core.UserPathHeader):
		return true
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

// copyPassthroughResponseHeaders copies upstream response headers to dst,
// excluding hop-by-hop and sensitive headers.
func copyPassthroughResponseHeaders(dst http.Header, src map[string][]string) {
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

// normalizePassthroughEndpoint strips the optional v1/ prefix from endpoint
// when the alias is enabled, and rejects it when disabled.
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
