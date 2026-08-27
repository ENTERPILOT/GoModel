package versioncheck

import "net/http"

// Beacon carries the allowlisted slice of a dashboard visit that travels with
// an update check.
type Beacon struct {
	UserAgent      string
	AcceptLanguage string
	ClientHints    map[string]string
	Visit          string
}

// maxForwardedValue bounds each forwarded browser header. /version is
// unauthenticated, so these values are supplied by whoever calls it; a cap
// keeps an oversized one out of the outbound request and the release host's
// records.
const maxForwardedValue = 512

// forwardedHeaders are the browser headers copied verbatim onto an update
// check. Anything absent from this list is dropped.
var forwardedHeaders = []string{
	"Sec-CH-UA",
	"Sec-CH-UA-Mobile",
	"Sec-CH-UA-Platform",
	"Sec-CH-UA-Platform-Version",
}

// BeaconFromRequest extracts the allowlisted fields of a dashboard request.
func BeaconFromRequest(r *http.Request, visit string) Beacon {
	beacon := Beacon{
		UserAgent:      bound(r.Header.Get("User-Agent")),
		AcceptLanguage: bound(r.Header.Get("Accept-Language")),
		Visit:          visit,
	}
	for _, name := range forwardedHeaders {
		if value := bound(r.Header.Get(name)); value != "" {
			if beacon.ClientHints == nil {
				beacon.ClientHints = make(map[string]string, len(forwardedHeaders))
			}
			beacon.ClientHints[name] = value
		}
	}
	return beacon
}

func bound(value string) string {
	if len(value) > maxForwardedValue {
		return value[:maxForwardedValue]
	}
	return value
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
