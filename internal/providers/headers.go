package providers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

// HeaderOverridesConfig holds per-provider header configuration.
type HeaderOverridesConfig struct {
	// CustomUpstreamHeaders adds static headers to all provider requests.
	CustomUpstreamHeaders map[string]string

	// PassthroughUserHeaders forwards all user headers to upstream.
	PassthroughUserHeaders bool

	// SkipHeaders prevents forwarding specific headers to upstream.
	SkipHeaders []string

	// SkipMode determines how SkipHeaders works: "skip" or "allow".
	SkipMode string
}

// passthroughCtxKey is the context key for storing passthrough headers.
type passthroughCtxKey struct{}

// WithPassthroughHeaders stores headers in context for passthrough.
func WithPassthroughHeaders(ctx context.Context, h http.Header) context.Context {
	return context.WithValue(ctx, passthroughCtxKey{}, h)
}

// PassthroughHeadersFromContext retrieves passthrough headers from context.
func PassthroughHeadersFromContext(ctx context.Context) http.Header {
	if h, ok := ctx.Value(passthroughCtxKey{}).(http.Header); ok {
		return h
	}
	return nil
}

// IsHeaderBlocked reports whether a header should be blocked from forwarding.
// Hard-coded skip floor: blocks credential headers, X-GoModel-User-Path, and configured alias.
func IsHeaderBlocked(name string, userPathAlias string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))

	// Credential headers are always blocked
	if core.IsCredentialHeader(name) {
		return true
	}

	// Internal header is always blocked
	if lower == "x-gomodel-user-path" {
		return true
	}

	// Block configured alias
	if userPathAlias != "" {
		aliasLower := strings.ToLower(strings.TrimSpace(userPathAlias))
		if lower == aliasLower {
			return true
		}
	}

	return false
}

// FilterIncomingHeaders returns a filtered copy of headers, removing blocked ones.
func FilterIncomingHeaders(headers http.Header, userPathAlias string) http.Header {
	filtered := make(http.Header)
	for name, values := range headers {
		if !IsHeaderBlocked(name, userPathAlias) {
			filtered[name] = values
		}
	}
	return filtered
}

// ApplyHeaderOverrides applies header overrides to the request.
// Main entry point: applies passthrough or static headers based on configuration.
func ApplyHeaderOverrides(req *http.Request, cfg HeaderOverridesConfig, userPathAlias string) {
	// No-op if neither passthrough nor custom headers are configured
	if !cfg.PassthroughUserHeaders && len(cfg.CustomUpstreamHeaders) == 0 {
		return
	}

	if cfg.PassthroughUserHeaders {
		// Log if custom headers ignored due to passthrough
		if len(cfg.CustomUpstreamHeaders) > 0 {
			slog.Debug("custom_upstream_headers ignored because passthrough_user_headers is active")
		}
		applyPassthroughHeaders(req, cfg, userPathAlias)
	} else if len(cfg.CustomUpstreamHeaders) > 0 {
		applyStaticHeaders(req, cfg.CustomUpstreamHeaders, userPathAlias)
	}
}

// applyPassthroughHeaders reads headers from request context, applies skip/allow list, sets headers.
func applyPassthroughHeaders(req *http.Request, cfg HeaderOverridesConfig, userPathAlias string) {
	source := PassthroughHeadersFromContext(req.Context())
	if source == nil {
		return
	}

	skipSet := normalizeHeaderSet(cfg.SkipHeaders)

	for name, values := range source {
		if shouldForward(name, skipSet, cfg.SkipMode, userPathAlias) {
			// Del first so forwarded values overwrite any provider defaults already on req.Header.
			req.Header.Del(name)
			for _, v := range values {
				req.Header.Add(name, v)
			}
		}
	}
}

// applyStaticHeaders adds static custom headers to request, skipping blocked names.
func applyStaticHeaders(req *http.Request, headers map[string]string, userPathAlias string) {
	for name, value := range headers {
		if !IsHeaderBlocked(name, userPathAlias) {
			req.Header.Set(name, value)
		}
	}
}

// shouldForward reports whether header should be forwarded based on skip/allow configuration.
// Checks floor (hard-coded blocks) first, then applies skip/allow list based on mode.
func shouldForward(name string, skipSet map[string]bool, mode string, userPathAlias string) bool {
	// Hard-coded floor: check blocked headers first
	if IsHeaderBlocked(name, userPathAlias) {
		return false
	}

	// Apply skip/allow list based on mode
	lower := strings.ToLower(strings.TrimSpace(name))
	switch mode {
	case "allow", "only":
		// Allow/only mode: only forward if header is in skipSet
		return skipSet[lower]
	case "skip", "":
		// Skip mode: forward if header is NOT in skipSet
		return !skipSet[lower]
	default:
		// Unknown mode: conservative default to skip
		return !skipSet[lower]
	}
}

// normalizeHeaderSet converts header list to case-insensitive lookup set.
func normalizeHeaderSet(headers []string) map[string]bool {
	result := make(map[string]bool)
	for _, h := range headers {
		lower := strings.ToLower(strings.TrimSpace(h))
		result[lower] = true
	}
	return result
}
