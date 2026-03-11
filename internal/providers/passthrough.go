package providers

import (
	"net/http"
	"strings"
)

// PassthroughEndpoint normalizes a provider-relative passthrough endpoint into
// an absolute path fragment suitable for baseURL + endpoint request building.
func PassthroughEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "/"
	}
	if strings.HasPrefix(endpoint, "/") {
		return endpoint
	}
	return "/" + endpoint
}

// CloneHTTPHeaders returns a detached copy of an http.Header map.
func CloneHTTPHeaders(src http.Header) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for key, values := range src {
		cloned := make([]string, len(values))
		copy(cloned, values)
		dst[key] = cloned
	}
	return dst
}
