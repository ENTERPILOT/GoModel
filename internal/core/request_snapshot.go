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
	// UserPath is the canonical business hierarchy path sourced from
	// X-GoModel-User-Path when provided.
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

// NewRequestSnapshotWithOwnedBody constructs a RequestSnapshot that takes
// ownership of capturedBody without cloning it. Callers must ensure the slice
// will not be mutated after passing it here.
func NewRequestSnapshotWithOwnedBody(method, path string, routeParams map[string]string, queryParams, headers map[string][]string, contentType string, capturedBody []byte, bodyNotCaptured bool, requestID string, traceMetadata map[string]string, userPath ...string) *RequestSnapshot {
	return newRequestSnapshot(method, path, routeParams, queryParams, headers, contentType, capturedBody, bodyNotCaptured, requestID, traceMetadata, false, userPath...)
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
