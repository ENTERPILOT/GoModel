package providers

import "strings"

// ResolveBaseURL returns the configured base URL when present, otherwise the provider default.
func ResolveBaseURL(baseURL, fallback string) string {
	if strings.TrimSpace(baseURL) == "" {
		return fallback
	}
	return baseURL
}

// ResolveAPIVersion returns the configured API version when present, otherwise the provider default.
func ResolveAPIVersion(apiVersion, fallback string) string {
	if strings.TrimSpace(apiVersion) == "" {
		return fallback
	}
	return apiVersion
}
