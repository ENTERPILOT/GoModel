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
	// every attempt the request makes. The host provides the map; call Set on
	// it before the call runs rather than assigning a new one, which would
	// drop headers earlier plugins in the chain wrote. The host folds what a
	// hook wrote into the request state when the phase finishes. A host may
	// refuse headers it owns. The GoModel host refuses credentials, the ones
	// that frame the connection or describe the body, Accept-Encoding,
	// Anthropic-Beta and Idempotency-Key, and drops invalid names and values.
	// It sends the rest only on provider calls for client requests that go
	// through its HTTP client: not on the gateway's own inference, such as a
	// guardrail judge, and not on the bedrock provider, which calls Bedrock
	// Runtime through the AWS SDK (bedrock-mantle carries them).
	Upstream http.Header
}
