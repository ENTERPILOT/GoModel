package versioncheck

import "net/http"

// Beacon carries the small, non-identifying slice of a dashboard visit that
// travels with an update check.
//
// It is built by an explicit allowlist (see BeaconFromRequest): the browser's
// Cookie, Authorization, API-key, and Referer headers are never copied, so a
// dashboard session credential cannot reach the release host.
//
// Two things are deliberately absent. The dashboard's own hostname would
// identify the operator's organization outright — a far stronger signal than
// anything else here, and one no retention rule would ever erase. Client
// addresses are personal data under GDPR, and for an internal dashboard they
// are usually a private LAN address that says nothing anyway.
type Beacon struct {
	// UserAgent is the browser's User-Agent, forwarded verbatim.
	UserAgent string
	// AcceptLanguage is the browser's language preference.
	AcceptLanguage string
	// ClientHints holds the forwarded Sec-CH-UA* hints, keyed by header name.
	ClientHints map[string]string
	// Visit is the raw YYYY-MM-DD-{id} cookie value identifying the browser
	// and the day it last checked.
	Visit string
}

// forwardedHeaders are the browser headers copied verbatim onto an update
// check. Everything absent from this list — Cookie, Authorization, X-API-Key,
// Referer, and any header a future browser adds — is dropped.
var forwardedHeaders = []string{
	"Sec-CH-UA",
	"Sec-CH-UA-Mobile",
	"Sec-CH-UA-Platform",
	"Sec-CH-UA-Platform-Version",
}

// BeaconFromRequest extracts the allowlisted fields of a dashboard request.
func BeaconFromRequest(r *http.Request, visit string) Beacon {
	beacon := Beacon{
		UserAgent:      r.Header.Get("User-Agent"),
		AcceptLanguage: r.Header.Get("Accept-Language"),
		Visit:          visit,
	}
	for _, name := range forwardedHeaders {
		if value := r.Header.Get(name); value != "" {
			if beacon.ClientHints == nil {
				beacon.ClientHints = make(map[string]string, len(forwardedHeaders))
			}
			beacon.ClientHints[name] = value
		}
	}
	return beacon
}

// dashboard reports whether the beacon came from a browser visit rather than
// the background schedule.
func (b Beacon) dashboard() bool {
	return b.UserAgent != "" || b.Visit != ""
}

// apply writes the beacon onto an outbound manifest request.
func (b Beacon) apply(req *http.Request) {
	source := "scheduled"
	if b.dashboard() {
		source = "dashboard"
	}
	req.Header.Set("X-GoModel-Source", source)
	if b.UserAgent != "" {
		req.Header.Set("User-Agent", b.UserAgent)
	}
	if b.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", b.AcceptLanguage)
	}
	for name, value := range b.ClientHints {
		req.Header.Set(name, value)
	}
	if b.Visit != "" {
		req.Header.Set("X-GoModel-Date", b.Visit)
	}
}
