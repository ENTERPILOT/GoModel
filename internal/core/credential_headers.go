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

// maxCredentialHeaderLen bounds the lowercase scratch buffer below; every
// credentialHeaders key is shorter, so longer names can never match.
const maxCredentialHeaderLen = 32

// IsCredentialHeader reports whether the header name carries credentials.
// Matching is case-insensitive and ignores surrounding whitespace. It runs
// for every header of every audited request, so the fold happens in a stack
// buffer rather than through strings.ToLower.
func IsCredentialHeader(name string) bool {
	name = strings.TrimSpace(name)
	if len(name) > maxCredentialHeaderLen {
		return false
	}
	var lower [maxCredentialHeaderLen]byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	_, ok := credentialHeaders[string(lower[:len(name)])]
	return ok
}
