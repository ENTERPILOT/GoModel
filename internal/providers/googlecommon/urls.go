package googlecommon

import (
	"net/url"
	"strings"
)

// VertexBaseURLs derives Vertex AI's two base URLs (OpenAI-compatible endpoint
// + native publisher endpoint) from either an operator-supplied baseURL or the
// project/location pair. When baseURL is empty the canonical aiplatform.googleapis.com
// host is used. When baseURL is supplied:
//   - if it ends in /endpoints/openapi it is treated as the OpenAI-compatible
//     surface and the native URL is derived by replacing the suffix
//   - if it ends in /publishers/google it is treated as the native surface and
//     the OpenAI-compatible URL is derived likewise
//   - otherwise both bases are set to the verbatim baseURL (caller has wired
//     up a custom proxy and is responsible for routing both shapes through it)
//
// Returning two strings rather than mutating a *Config keeps this pure and
// trivially testable; callers unpack their own provider config.
func VertexBaseURLs(baseURL, project, location string) (openAICompatibleBaseURL, nativeBaseURL string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		project = strings.TrimSpace(project)
		location = strings.TrimSpace(location)
		root := "https://aiplatform.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location)
		return root + "/endpoints/openapi", root + "/publishers/google"
	}
	if nativeBaseURL, ok := VertexNativeBaseURLFromOpenAICompatibleBaseURL(baseURL); ok {
		return baseURL, nativeBaseURL
	}
	if openAIBaseURL, ok := VertexOpenAICompatibleBaseURLFromNativeBaseURL(baseURL); ok {
		return openAIBaseURL, baseURL
	}
	return baseURL, baseURL
}

// VertexNativeBaseURLFromOpenAICompatibleBaseURL converts an
// /endpoints/openapi base URL into the matching /publishers/google native URL.
// Returns ok=false when the input does not end with /endpoints/openapi.
func VertexNativeBaseURLFromOpenAICompatibleBaseURL(baseURL string) (string, bool) {
	const suffix = "/endpoints/openapi"
	if !strings.HasSuffix(baseURL, suffix) {
		return "", false
	}
	root := strings.TrimRight(strings.TrimSuffix(baseURL, suffix), "/")
	if root == "" {
		return "", false
	}
	return root + "/publishers/google", true
}

// VertexOpenAICompatibleBaseURLFromNativeBaseURL converts a /publishers/google
// native base URL into the matching /endpoints/openapi OpenAI-compatible URL.
// Returns ok=false when the input does not end with /publishers/google.
func VertexOpenAICompatibleBaseURLFromNativeBaseURL(baseURL string) (string, bool) {
	const suffix = "/publishers/google"
	if !strings.HasSuffix(baseURL, suffix) {
		return "", false
	}
	root := strings.TrimRight(strings.TrimSuffix(baseURL, suffix), "/")
	if root == "" {
		return "", false
	}
	return root + "/endpoints/openapi", true
}
