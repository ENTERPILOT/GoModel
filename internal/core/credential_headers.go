package core

import "strings"

// credentialHeaders lists HTTP headers whose values carry secrets (API keys,
// tokens, cookies). It is the single source of truth for audit-log header
// redaction and for rejecting these headers as tagging label sources.
var credentialHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"api-key":             {}, // Azure OpenAI credential header
	"x-goog-api-key":      {}, // Google Gemini / Vertex credential header
	"x-auth-token":        {},
	"x-access-token":      {},
	"x-gomodel-key":       {},
}

// IsCredentialHeader reports whether the header name carries credentials.
// Matching is case-insensitive and ignores surrounding whitespace.
func IsCredentialHeader(name string) bool {
	_, ok := credentialHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok
}
