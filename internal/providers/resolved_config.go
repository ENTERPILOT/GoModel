package providers

import "strings"

// ResolveBaseURL returns the configured base URL when present, otherwise the provider default.
func ResolveBaseURL(baseURL, fallback string) string {
	if strings.TrimSpace(baseURL) == "" {
		return fallback
	}
	return baseURL
}
