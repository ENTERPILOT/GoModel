package core

import (
	"maps"
	"strings"
)

// RequestSnapshot is the transport-level capture of an inbound request. It
// preserves the request as received at the HTTP boundary so later stages can
// extract semantics without losing fidelity while keeping mutable state behind
// defensive-copy accessors by default.
type RequestSnapshot struct {
	// Method is the inbound HTTP method.
	Method string
	// Path is the request URL path as received at ingress.
	Path string
	// UserPath is the canonical business hierarchy path sourced from the
	// configured user-path request header when provided.
	UserPath string
	// RouteParams contains resolved router parameters such as provider or file id.
	routeParams map[string]string
	// QueryParams contains the raw query string values by key.
	queryParams map[string][]string
	// Headers contains the inbound HTTP headers exactly as captured at ingress.
	headers map[string][]string
	// ContentType is the inbound Content-Type header value.
	ContentType string
	// RawBody contains the captured request body bytes when the body fit within
	// the ingress capture limit.
	capturedBody []byte
	// BodyNotCaptured reports that the request body exceeded the capture limit,
	// so CapturedBody is omitted and the live body stream remains on the request.
	BodyNotCaptured bool
	// RequestID is the canonical request id propagated through context, headers,
	// providers, and audit records for this request.
	RequestID string
	// TraceMetadata contains tracing-related key/value pairs such as trace/span
	// ids or baggage/sampling metadata derived from tracing headers.
	traceMetadata map[string]string
}

// NewRequestSnapshot constructs a RequestSnapshot and defensively copies its
// mutable map and byte-slice inputs.
func NewRequestSnapshot(method, path string, routeParams map[string]string, queryParams, headers map[string][]string, contentType string, capturedBody []byte, bodyNotCaptured bool, requestID string, traceMetadata map[string]string, userPath ...string) *RequestSnapshot {
	return newRequestSnapshot(method, path, routeParams, queryParams, headers, contentType, capturedBody, bodyNotCaptured, requestID, traceMetadata, true, userPath...)
}

// NewRequestSnapshotWithOwnedMaps constructs a RequestSnapshot that takes
// ownership of routeParams, queryParams, traceMetadata, and capturedBody
// (callers must not mutate them afterwards) while still defensively cloning
// headers, which is typically the live request header map mutated downstream.
//
// Use this on the ingress hot path, where the route/query/trace maps and body
// are freshly built for the snapshot and would otherwise be cloned for no benefit.
func NewRequestSnapshotWithOwnedMaps(method, path string, routeParams map[string]string, queryParams, headers map[string][]string, contentType string, capturedBody []byte, bodyNotCaptured bool, requestID string, traceMetadata map[string]string, userPath ...string) *RequestSnapshot {
	return &RequestSnapshot{
		Method:          method,
		Path:            path,
		UserPath:        firstUserPath(userPath),
		routeParams:     routeParams,
		queryParams:     queryParams,
		headers:         cloneMultiMap(headers),
		ContentType:     contentType,
		capturedBody:    capturedBody,
		BodyNotCaptured: bodyNotCaptured,
		RequestID:       requestID,
		traceMetadata:   traceMetadata,
	}
}

func newRequestSnapshot(method, path string, routeParams map[string]string, queryParams, headers map[string][]string, contentType string, capturedBody []byte, bodyNotCaptured bool, requestID string, traceMetadata map[string]string, cloneBody bool, userPath ...string) *RequestSnapshot {
	body := capturedBody
	if cloneBody {
		body = cloneBytes(capturedBody)
	}
	return &RequestSnapshot{
		Method:          method,
		Path:            path,
		UserPath:        firstUserPath(userPath),
		routeParams:     cloneStringMap(routeParams),
		queryParams:     cloneMultiMap(queryParams),
		headers:         cloneMultiMap(headers),
		ContentType:     contentType,
		capturedBody:    body,
		BodyNotCaptured: bodyNotCaptured,
		RequestID:       requestID,
		traceMetadata:   cloneStringMap(traceMetadata),
	}
}

func firstUserPath(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// WithUserPath returns a shallow-cloned snapshot with UserPath and the captured
// default user-path header rewritten to the provided canonical value.
func (s *RequestSnapshot) WithUserPath(userPath string) *RequestSnapshot {
	return s.WithUserPathHeader(userPath, UserPathHeader)
}

// WithUserPathHeader returns a shallow-cloned snapshot with UserPath and the
// captured configured user-path header rewritten to the provided canonical value.
func (s *RequestSnapshot) WithUserPathHeader(userPath, headerName string) *RequestSnapshot {
	if s == nil {
		return nil
	}
	headerName = UserPathHeaderName(headerName)
	cloned := *s
	cloned.UserPath = strings.TrimSpace(userPath)
	cloned.headers = cloneMultiMap(s.headers)
	if cloned.UserPath == "" {
		if cloned.headers != nil {
			delete(cloned.headers, headerName)
		}
		return &cloned
	}
	if cloned.headers == nil {
		cloned.headers = make(map[string][]string, 1)
	}
	cloned.headers[headerName] = []string{cloned.UserPath}
	return &cloned
}

// WithOwnedCapturedBody returns a shallow-cloned snapshot with request body
// capture state replaced. capturedBody is taken as owned by the snapshot and
// must not be mutated after this call.
func (s *RequestSnapshot) WithOwnedCapturedBody(capturedBody []byte, bodyNotCaptured bool) *RequestSnapshot {
	if s == nil {
		return nil
	}
	cloned := *s
	cloned.capturedBody = capturedBody
	cloned.BodyNotCaptured = bodyNotCaptured
	return &cloned
}

// The snapshot is immutable after construction and exposes its body, headers,
// and parameter maps two ways: a Get*/CapturedBody accessor that returns a
// defensive copy (for callers that need an independently mutable value), and a
// *View accessor that returns the underlying value with no allocation (for
// read-only callers). The request hot path uses the View accessors; the copying
// accessors exist for callers that mutate the result.

// CapturedBody returns a defensive copy of the captured request body bytes.
func (s *RequestSnapshot) CapturedBody() []byte {
	if s == nil {
		return nil
	}
	return cloneBytes(s.capturedBody)
}

// CapturedBodyView returns the captured request body bytes without cloning.
// Callers must treat the returned slice as read-only.
func (s *RequestSnapshot) CapturedBodyView() []byte {
	if s == nil {
		return nil
	}
	return s.capturedBody
}

// GetRouteParams returns a defensive copy of the captured route parameters.
func (s *RequestSnapshot) GetRouteParams() map[string]string {
	if s == nil {
		return nil
	}
	return cloneStringMap(s.routeParams)
}

// GetQueryParams returns a defensive copy of the captured query parameters.
func (s *RequestSnapshot) GetQueryParams() map[string][]string {
	if s == nil {
		return nil
	}
	return cloneMultiMap(s.queryParams)
}

// GetHeaders returns a defensive copy of the captured request headers.
func (s *RequestSnapshot) GetHeaders() map[string][]string {
	if s == nil {
		return nil
	}
	return cloneMultiMap(s.headers)
}

// HeadersView returns the captured request headers without cloning. Callers
// must treat the returned map as read-only.
func (s *RequestSnapshot) HeadersView() map[string][]string {
	if s == nil {
		return nil
	}
	return s.headers
}

// GetTraceMetadata returns a defensive copy of the captured trace metadata.
func (s *RequestSnapshot) GetTraceMetadata() map[string]string {
	if s == nil {
		return nil
	}
	return cloneStringMap(s.traceMetadata)
}

func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func cloneMultiMap(src map[string][]string) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for key, values := range src {
		if len(values) == 0 {
			dst[key] = nil
			continue
		}
		cloned := make([]string, len(values))
		copy(cloned, values)
		dst[key] = cloned
	}
	return dst
}
