package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPassthroughBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "strips the /v1 suffix", baseURL: "http://host:8000/v1", want: "http://host:8000"},
		{name: "strips a trailing slash first", baseURL: "http://host:8000/v1/", want: "http://host:8000"},
		{name: "trims surrounding space", baseURL: "  http://host:8000/v1  ", want: "http://host:8000"},
		{name: "leaves a root base URL alone", baseURL: "http://host:8000", want: "http://host:8000"},
		{name: "only the suffix counts", baseURL: "http://host:8000/v1/extra", want: "http://host:8000/v1/extra"},
		{name: "empty", baseURL: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PassthroughBaseURL(tt.baseURL))
		})
	}
}

// The prefix list belongs to each upstream, but the matching rules are shared.
func TestUsesV1PassthroughBase(t *testing.T) {
	prefixes := []string{"/models", "/chat/completions", "/embeddings"}

	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{name: "exact prefix", endpoint: "/models", want: true},
		{name: "without a leading slash", endpoint: "models", want: true},
		{name: "below a prefix", endpoint: "/models/gpt-5", want: true},
		{name: "a longer sibling is not a match", endpoint: "/models-extra", want: false},
		{name: "a root path the upstream serves itself", endpoint: "/tokenize", want: false},
		{name: "already addressed under /v1", endpoint: "/v1/models", want: false},
		// The server appends the request's raw query to the endpoint, so the
		// query has to come off before the path is classified.
		{name: "query string on a v1 endpoint", endpoint: "/models?limit=10", want: true},
		{name: "query string below a prefix", endpoint: "/models/gpt-5?verbose=1", want: true},
		{name: "query string on a root path", endpoint: "/tokenize?fast=1", want: false},
		{name: "query string on an explicit /v1 path", endpoint: "/v1/models?limit=10", want: false},
		{name: "empty endpoint", endpoint: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, UsesV1PassthroughBase(tt.endpoint, prefixes))
		})
	}
}

func TestUsesV1PassthroughBaseWithoutPrefixes(t *testing.T) {
	assert.False(t, UsesV1PassthroughBase("/models", nil))
}
