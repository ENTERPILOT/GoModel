package mcpgateway

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// originGuard decides whether one downstream request may reach the MCP
// handler, and is the gateway's DNS-rebinding defense.
//
// The threat is a browser: an attacker's page points its own hostname at the
// gateway's address, and the browser then issues what it believes are
// same-origin requests to it. Rebinding changes only which address a hostname
// resolves to, never the hostname itself, so a rebound request's Origin and
// Host still agree with each other. Comparing the two — the fallback in
// http.CrossOriginProtection, which this guard replaces — therefore cannot
// detect a rebind, and neither can Sec-Fetch-Site, which reads "same-origin"
// for exactly the same reason. Only an explicit allowlist distinguishes the
// gateway's real origin from an attacker's rebound one.
//
// So the rule is: a request a browser initiated is served only when its
// Origin is explicitly trusted. Requests carrying no browser fetch metadata
// are ordinary MCP client traffic and pass through — still subject to the
// gateway's bearer authentication, which is the control that keeps unknown
// non-browser callers out.
//
// Unlike http.CrossOriginProtection this applies to every method. GET opens
// the MCP notification stream and DELETE tears a session down; neither is a
// "safe method" in the sense that would justify exempting it.
type originGuard struct {
	trusted   map[string]struct{}
	hosts     map[string]struct{}
	trustsAll bool
}

// newOriginGuard compiles the configured allowlist. Entries are written the
// way an Origin header is ("scheme://host[:port]"), or config.TrustAnyOrigin
// to disable the check. An empty allowlist trusts no browser origin, which is
// the default and is correct for the MCP clients this endpoint exists to
// serve — none of them are web pages.
func newOriginGuard(origins []string) (*originGuard, error) {
	guard := &originGuard{
		trusted: make(map[string]struct{}, len(origins)),
		hosts:   make(map[string]struct{}, len(origins)),
	}
	for _, raw := range origins {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if entry == config.TrustAnyOrigin {
			guard.trustsAll = true
			continue
		}
		normalized, err := config.NormalizeAllowedOrigin(entry)
		if err != nil {
			return nil, err
		}
		guard.trusted[normalized] = struct{}{}
		guard.hosts[config.OriginHost(normalized)] = struct{}{}
	}
	return guard, nil
}

// sameOriginStreamFromTrustedHost recognizes the one browser request that
// legitimately carries no Origin: the GET that opens the MCP notification
// stream. Per the Fetch standard a browser appends Origin only to CORS-tainted
// requests and to unsafe methods, so a same-origin GET or HEAD arrives with
// Sec-Fetch-Site but no Origin. Without this the allowlist would be useless —
// a browser client could initialize over POST and then never open its stream.
//
// Matching Host against the allowlist is sound here, and is not the broken
// self-consistency check this guard replaced. That one compared two
// attacker-supplied values (Origin and Host) to each other, which a rebound
// request satisfies by construction. This compares Host to an operator-
// configured list the attacker cannot write to: "same-origin" is the browser
// attesting that the initiating page's origin is the request's origin, whose
// host is the Host header, so a rebound page at attacker.example presents
// Host: attacker.example and matches nothing. An empty allowlist — the
// default — matches nothing either, so this widens nothing out of the box.
func (g *originGuard) sameOriginStreamFromTrustedHost(r *http.Request, fetchSite string) bool {
	// "same-site" and "none" are excluded deliberately: neither tells us the
	// initiating page's origin equals the request's, so Host proves nothing.
	if !strings.EqualFold(fetchSite, "same-origin") {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	_, ok := g.hosts[strings.ToLower(strings.TrimSpace(r.Host))]
	return ok
}

// check returns the error to fail a request with, or nil to serve it.
func (g *originGuard) check(r *http.Request) error {
	if g == nil {
		// An unconfigured guard trusts nothing rather than everything: this is
		// the gateway's rebinding defense, so it fails closed.
		g = &originGuard{}
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	fetchSite := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))

	// Neither header means no browser was involved. Both are forbidden header
	// names, so page JavaScript can neither strip nor forge them; their joint
	// absence is a reliable signal, and every browser has sent Sec-Fetch-Site
	// since 2023.
	if origin == "" && fetchSite == "" {
		return nil
	}
	if g.trustsAll {
		return nil
	}
	if origin != "" {
		// A malformed Origin, or the literal "null" that opaque origins such
		// as sandboxed frames send, simply fails to match any entry.
		if normalized, err := config.NormalizeAllowedOrigin(origin); err == nil {
			if _, ok := g.trusted[normalized]; ok {
				return nil
			}
		}
	} else if g.sameOriginStreamFromTrustedHost(r, fetchSite) {
		return nil
	}
	// The rejected Origin is deliberately not echoed back: it is
	// attacker-controlled text, and the caller already knows what it sent.
	return core.NewInvalidRequestErrorWithStatus(http.StatusForbidden,
		"browser requests to the MCP endpoint are not permitted from this origin; "+
			"list it in mcp.allowed_origins (MCP_ALLOWED_ORIGINS) to allow it", nil)
}
