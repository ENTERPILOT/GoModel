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
	"x-anthropic-api-key": {},
	"x-auth-token":        {},
	"x-access-token":      {},
	"x-gomodel-key":       {},
	"xi-api-key":          {}, // ElevenLabs credential header
	// Keys of the AI gateways and API managers a provider base URL can point
	// at, which authenticate the gateway to the proxy rather than the provider.
	"helicone-auth":             {},
	"cf-aig-authorization":      {}, // Cloudflare AI Gateway
	"x-portkey-api-key":         {},
	"ocp-apim-subscription-key": {}, // Azure API Management
}

// maxCredentialHeaderLen bounds the lowercase scratch buffer below; every
// credentialHeaders key is shorter, so longer names can never match.
const maxCredentialHeaderLen = 32

// IsCredentialHeader reports whether the header name carries credentials.
// Matching is case-insensitive, ignores surrounding whitespace, and treats
// "_" as "-": CGI-style servers and some proxies fold one into the other, so
// X_Api_Key reaches them as x-api-key. It runs
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
		switch {
		case 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		case c == '_':
			c = '-'
		}
		lower[i] = c
	}
	_, ok := credentialHeaders[string(lower[:len(name)])]
	return ok
}
