package providers

import (
	"context"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

// ApplyRequestHeaderOverrides applies per-provider header rules to h. The two
// modes are mutually exclusive: setting both panics. Config-load validation in
// resolveProviders should prevent reaching it; the panic is the backstop so
// internal callers never produce an ambiguous outbound header mix.
//
// With passthrough true, every non-skipped inbound header from the request
// snapshot overwrites its key on h (inbound wins over provider defaults; an
// empty inbound value removes the key). With passthrough false, customHeaders
// is applied verbatim and inbound headers are ignored. Provider-set auth
// headers are written by the factory before this call and are left alone.
func ApplyRequestHeaderOverrides(ctx context.Context, h http.Header, customHeaders map[string]string, passthrough bool, extras ...string) {
	if h == nil {
		return
	}

	hasCustom := len(customHeaders) > 0
	if hasCustom && passthrough {
		panic("ApplyRequestHeaderOverrides: passthrough_user_headers and custom_upstream_headers are mutually exclusive; the config-loader should have rejected this")
	}

	if passthrough {
		snapshot := core.GetRequestSnapshot(ctx)
		if snapshot == nil {
			return
		}
		headers := snapshot.HeadersView()
		for key, values := range headers {
			if SkipPassthroughHeader(key, extras...) {
				continue
			}
			canonicalKey := http.CanonicalHeaderKey(key)
			if canonicalKey == "" {
				continue
			}
			h.Del(canonicalKey)
			for _, value := range values {
				h.Add(canonicalKey, value)
			}
		}
		return
	}

	if !hasCustom {
		return
	}
	for key, value := range customHeaders {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonicalKey == "" {
			continue
		}
		h.Set(canonicalKey, value)
	}
}
