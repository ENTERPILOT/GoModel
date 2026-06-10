package providers

import (
	"net/url"
	"strconv"
)

// PaginatedEndpoint appends optional limit and pagination-cursor query
// parameters to an endpoint path. The cursor parameter name varies by
// provider (e.g. "after" for OpenAI-compatible APIs, "before_id" for
// Anthropic).
func PaginatedEndpoint(path string, limit int, cursorParam, cursor string) string {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		values.Set(cursorParam, cursor)
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}
