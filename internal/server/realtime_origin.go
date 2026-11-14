package server

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// realtimeOriginGuard decides whether a realtime websocket upgrade may
// proceed, from its Origin header.
//
// A websocket handshake is not covered by the browser's same-origin policy or
// by CORS: any page may open one to the gateway, and the browser attaches the
// ambient credentials it holds for it. A page cannot add an Authorization
// header to a handshake, so the upgrades at stake are the ones that are
// accepted without one: a gateway running without authentication, or a
// session an authentication extension recognizes.
//
// Only browsers send Origin on a handshake, and they always do. So a request
// without one is an SDK, a server, or a CLI tool and is served; a page served
// from the gateway's own host is served; any other origin has to be listed.
type realtimeOriginGuard struct {
	trusted   map[string]struct{}
	trustsAll bool
}

// newRealtimeOriginGuard compiles server.realtime_allowed_origins. Entries
// are written the way an Origin header is ("scheme://host[:port]"), or
// config.TrustAnyOrigin to turn the check off. An entry that is not a valid
// origin matches nothing.
func newRealtimeOriginGuard(origins []string) *realtimeOriginGuard {
	guard := &realtimeOriginGuard{trusted: make(map[string]struct{}, len(origins))}
	for _, raw := range origins {
		entry := strings.TrimSpace(raw)
		if entry == config.TrustAnyOrigin {
			guard.trustsAll = true
			continue
		}
		if normalized, err := config.NormalizeAllowedOrigin(entry); err == nil {
			guard.trusted[normalized] = struct{}{}
		}
	}
	return guard
}

// check returns the error to refuse an upgrade with, or nil to serve it. A
// nil guard allows no cross-origin upgrade.
func (g *realtimeOriginGuard) check(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || (g != nil && g.trustsAll) {
		return nil
	}
	// A malformed Origin, or the literal "null" that opaque origins such as
	// sandboxed frames send, matches neither the request host nor an entry.
	if normalized, err := config.NormalizeAllowedOrigin(origin); err == nil {
		if sameHost(normalized, r.Host) {
			return nil
		}
		if g != nil {
			if _, ok := g.trusted[normalized]; ok {
				return nil
			}
		}
	}
	// The rejected Origin is not echoed back: it is caller-controlled text.
	return core.NewInvalidRequestErrorWithStatus(http.StatusForbidden,
		"realtime websocket upgrades are not permitted from this origin; "+
			"list it in server.realtime_allowed_origins (REALTIME_ALLOWED_ORIGINS) to allow it", nil)
}

// sameHost reports whether a normalized origin names the request's Host. The
// Host is canonicalized under the origin's scheme first, so an explicit
// default port ("localhost:80") still matches "http://localhost".
func sameHost(normalizedOrigin, host string) bool {
	scheme, _, _ := strings.Cut(normalizedOrigin, "://")
	canonical, err := config.NormalizeAllowedOrigin(scheme + "://" + strings.TrimSpace(host))
	if err != nil {
		return false
	}
	return canonical == normalizedOrigin
}
