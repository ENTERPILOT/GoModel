package providers

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

// AuthHeaderConfig describes how a provider populates outbound request headers.
// It captures the few axes along which OpenAI-compatible providers differ so the
// shared logic (empty-key handling, request-ID forwarding, validation) lives in
// one place and each provider declares only its variations as data.
type AuthHeaderConfig struct {
	// AuthHeader carries the credential. Defaults to "Authorization".
	AuthHeader string
	// AuthScheme prefixes the credential, e.g. "Bearer ". Empty for raw values.
	AuthScheme string
	// RequestIDHeader, when non-empty, forwards the context request ID under
	// this header name. When empty, no request ID is forwarded.
	RequestIDHeader string
	// ValidateRequestID, when set, gates request-ID forwarding (e.g. ASCII and
	// length checks required by some upstreams).
	ValidateRequestID func(string) bool
	// OptionalAPIKey skips the auth header entirely when the API key is empty,
	// for providers that allow unauthenticated access (e.g. local Ollama/vLLM).
	OptionalAPIKey bool
}

// IsValidClientRequestID reports whether id may be forwarded as a client
// request-ID header value: upstreams that accept one (OpenAI, OpenRouter,
// Azure) require printable ASCII and reject oversized values with a 400.
func IsValidClientRequestID(id string) bool {
	if len(id) > 512 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7E {
			return false
		}
	}
	return true
}

// SetAuthHeaders applies cfg to req for the given API key. It is safe to use
// directly as an llmclient header hook or as CompatibleProviderConfig.SetHeaders.
func SetAuthHeaders(req *http.Request, apiKey string, cfg AuthHeaderConfig) {
	if apiKey != "" || !cfg.OptionalAPIKey {
		header := cfg.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, cfg.AuthScheme+apiKey)
	}

	if cfg.RequestIDHeader == "" {
		return
	}
	requestID := core.GetRequestID(req.Context())
	if requestID == "" {
		return
	}
	if cfg.ValidateRequestID != nil && !cfg.ValidateRequestID(requestID) {
		return
	}
	req.Header.Set(cfg.RequestIDHeader, requestID)
}
