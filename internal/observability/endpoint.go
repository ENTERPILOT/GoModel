package observability

import (
	"strings"
	"sync"
)

// maxEndpointLabels caps the distinct endpoint label values. Templating
// catches ID-like segments, but passthrough paths are caller-chosen, so any
// path beyond the cap is reported as otherEndpointLabel.
const (
	maxEndpointLabels  = 256
	otherEndpointLabel = "/{other}"
)

var endpointLabels = struct {
	sync.RWMutex
	seen map[string]struct{}
}{seen: map[string]struct{}{}}

// idCollections are upstream path segments followed by a resource ID
// (files, batches, stored responses, voices, models, ...).
var idCollections = map[string]bool{
	"batches":        true,
	"cachedContents": true,
	"files":          true,
	"jobs":           true,
	"models":         true,
	"responses":      true,
	"tasks":          true,
	"text-to-speech": true,
	"voices":         true,
}

// collectionActions are fixed sub-paths of an ID collection, not IDs.
var collectionActions = map[string]bool{
	"batches":      true,
	"compact":      true,
	"count_tokens": true,
	"input_tokens": true,
}

// metricEndpoint turns an upstream endpoint into a bounded metric label:
// the query string is dropped and resource IDs become "{id}", so
// "/files/file-abc/content?x=1" is reported as "/files/{id}/content".
func metricEndpoint(endpoint string) string {
	endpoint, _, _ = strings.Cut(endpoint, "?")
	endpoint = strings.Trim(endpoint, "/")
	if endpoint == "" {
		return "/"
	}
	segments := strings.Split(endpoint, "/")
	for i, segment := range segments {
		if i > 0 && idCollections[segments[i-1]] && !collectionActions[segment] {
			segments[i] = templateSegment(segment)
			continue
		}
		if looksLikeID(segment) {
			segments[i] = templateSegment(segment)
		}
	}
	return boundEndpointLabel("/" + strings.Join(segments, "/"))
}

// boundEndpointLabel admits a label until maxEndpointLabels distinct values
// are in use; a label once admitted keeps its value.
func boundEndpointLabel(label string) string {
	endpointLabels.RLock()
	_, ok := endpointLabels.seen[label]
	full := len(endpointLabels.seen) >= maxEndpointLabels
	endpointLabels.RUnlock()
	if ok {
		return label
	}
	if full {
		return otherEndpointLabel
	}
	endpointLabels.Lock()
	defer endpointLabels.Unlock()
	if _, ok := endpointLabels.seen[label]; ok {
		return label
	}
	if len(endpointLabels.seen) >= maxEndpointLabels {
		return otherEndpointLabel
	}
	endpointLabels.seen[label] = struct{}{}
	return label
}

// resetEndpointLabels forgets admitted labels, alongside a metrics reset.
func resetEndpointLabels() {
	endpointLabels.Lock()
	defer endpointLabels.Unlock()
	clear(endpointLabels.seen)
}

// templateSegment replaces an ID while keeping a Gemini-style ":action"
// suffix, e.g. "gemini-2.5-flash:generateContent" -> "{id}:generateContent".
func templateSegment(segment string) string {
	if _, action, ok := strings.Cut(segment, ":"); ok {
		return "{id}:" + action
	}
	return "{id}"
}

// looksLikeID catches IDs outside known collections, such as passthrough
// paths: all-digit segments and long segments that contain a digit.
func looksLikeID(segment string) bool {
	if segment == "" {
		return false
	}
	digits := 0
	for _, r := range segment {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits == len(segment) || (digits > 0 && len(segment) >= 16)
}
