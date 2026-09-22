package observability

import (
	"strings"
)

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
	return "/" + strings.Join(segments, "/")
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
