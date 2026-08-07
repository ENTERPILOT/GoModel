package core

import (
	"net/url"
	"strings"
)

var sensitiveURLQueryKeys = map[string]struct{}{
	"access_token":      {},
	"client_secret":     {},
	"code":              {},
	"code_challenge":    {},
	"code_verifier":     {},
	"error_description": {},
	"id_token":          {},
	"nonce":             {},
	"refresh_token":     {},
	"state":             {},
	"token":             {},
}

// RedactSensitiveURLQuery removes credential and authentication-transaction
// values from an absolute or relative URL while preserving ordinary query
// parameters. Malformed sensitive queries fail closed by dropping the query.
func RedactSensitiveURLQuery(raw string) string {
	// The model-serving hot path normally has no query string. Avoid URL parsing
	// and allocations unless there is something that may require redaction.
	if strings.IndexByte(raw, '?') < 0 {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return redactMalformedSensitiveQuery(raw)
	}
	if parsed.RawQuery == "" {
		return raw
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return redactMalformedSensitiveQuery(raw)
	}
	changed := false
	for key := range values {
		if _, sensitive := sensitiveURLQueryKeys[strings.ToLower(key)]; sensitive {
			values[key] = []string{"REDACTED"}
			changed = true
		}
	}
	if !changed {
		return raw
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func redactMalformedSensitiveQuery(raw string) string {
	queryAt := strings.IndexByte(raw, '?')
	if queryAt < 0 || !containsSensitiveURLQueryKey(raw[queryAt+1:]) {
		return raw
	}
	return raw[:queryAt]
}

func containsSensitiveURLQueryKey(rawQuery string) bool {
	for _, field := range strings.FieldsFunc(rawQuery, func(r rune) bool { return r == '&' || r == ';' }) {
		key, _, _ := strings.Cut(field, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		if _, sensitive := sensitiveURLQueryKeys[strings.ToLower(decoded)]; sensitive {
			return true
		}
	}
	return false
}
