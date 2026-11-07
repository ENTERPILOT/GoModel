package pluginapi

import "net/http"

// Headers carries the HTTP headers of an exchange.
type Headers struct {
	// Request is a copy of the inbound headers with credential headers
	// redacted. Edits affect upstream passthrough headers and the audit
	// record.
	Request http.Header
	// Response is applied to the client response: a header set here is sent
	// with the given values. A header set to the single empty string is
	// removed from the response instead, including headers the gateway adds
	// on its own.
	Response http.Header
	// Upstream adds headers to the provider call: one per request, applied to
	// every attempt the request makes. Assign a map before the call runs; the
	// host folds what a hook wrote into the request state when the phase
	// finishes. A host may refuse headers it owns, such as credentials, the ones
	// that frame the connection or describe the body, and provider feature
	// negotiation.
	Upstream http.Header
}
