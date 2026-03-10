package core

// IngressFrame is the immutable transport-level capture of an inbound request.
// It preserves the request as received at the HTTP boundary so later stages can
// extract semantics without losing fidelity.
type IngressFrame struct {
	Method        string
	Path          string
	RouteParams   map[string]string
	QueryParams   map[string][]string
	Headers       map[string][]string
	ContentType   string
	RawBody       []byte
	RequestID     string
	TraceMetadata map[string]string
}
