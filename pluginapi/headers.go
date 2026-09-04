package pluginapi

import "net/http"

// Headers carries the HTTP headers of an exchange.
type Headers struct {
	// Request is a copy of the inbound headers with credential headers
	// redacted. Edits affect upstream passthrough headers and the audit
	// record.
	Request http.Header
	// Response is appended to the client response.
	Response http.Header
	// Upstream adds headers to the provider call. A host that does not
	// support it ignores the field.
	Upstream http.Header
}
