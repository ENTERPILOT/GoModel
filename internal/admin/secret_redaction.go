package admin

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// A secret reference names where a secret lives and is not secret, so the
// admin API shows a secret field as stored when nothing else in it can be a
// secret. A value that mixes literal text with a reference
// ("sk-live-${env:SUFFIX}") may hold a literal secret, so it is masked like
// one, and sending the mask back keeps it.

// secretValueShown reports whether a secret field is made of references alone.
func secretValueShown(value string) bool {
	return config.OnlySecretReferences(value)
}

// httpAuthSchemes are the Authorization schemes an MCP header may name in
// front of a reference and still be shown: the scheme is not secret.
var httpAuthSchemes = map[string]struct{}{"bearer": {}, "basic": {}, "token": {}}

// mcpHeaderShown reports whether an MCP header value is made of references
// alone, optionally after an HTTP auth scheme ("Bearer ${env:TOKEN}").
func mcpHeaderShown(value string) bool {
	if config.OnlySecretReferences(value) {
		return true
	}
	scheme, rest, found := strings.Cut(value, " ")
	_, known := httpAuthSchemes[strings.ToLower(scheme)]
	return found && known && config.OnlySecretReferences(rest)
}

// redactProxyURL masks the password of a literal proxy URL. A proxy URL
// holding a reference is shown as stored when its password is made of
// references alone, the rest of a proxy URL being no secret; otherwise it is
// masked whole.
func redactProxyURL(value string) string {
	if !config.HasSecretReference(value) {
		return httpclient.RedactProxyURL(value)
	}
	if proxyPasswordIsReference(value) {
		return value
	}
	return redactedCredentialValue
}

// proxyPasswordIsReference reports whether the password of a proxy URL that
// holds references is absent or made of references alone. References are
// swapped for placeholders first, since their braces are not valid in a URL;
// the placeholders share a prefix that does not occur in value, so no literal
// text can pass for one. The password is read raw, before percent-decoding,
// for the same reason.
func proxyPasswordIsReference(value string) bool {
	parsable, placeholders := withReferencePlaceholders(value)
	if _, err := url.Parse(parsable); err != nil {
		return false
	}
	authority := parsable
	if _, after, found := strings.Cut(authority, "://"); found {
		authority = after
	}
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return true
	}
	_, password, set := strings.Cut(authority[:at], ":")
	if !set {
		return true
	}
	for password != "" {
		matched := false
		for _, placeholder := range placeholders {
			if rest, ok := strings.CutPrefix(password, placeholder); ok {
				password, matched = rest, true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// withReferencePlaceholders replaces each secret reference in value with an
// alphanumeric placeholder and returns the placeholders used. Every
// placeholder starts with a prefix absent from value and ends in "x", so none
// is a prefix of another or of any literal text.
func withReferencePlaceholders(value string) (string, []string) {
	prefix := "gomodelsecretref"
	for strings.Contains(value, prefix) {
		prefix += "q"
	}
	var placeholders []string
	var b strings.Builder
	for rest := value; rest != ""; {
		i := strings.Index(rest, "${")
		end := -1
		if i >= 0 {
			end = strings.IndexByte(rest[i:], '}')
		}
		if i < 0 || end < 0 {
			b.WriteString(rest)
			break
		}
		token := rest[i : i+end+1]
		escaped := i > 0 && rest[i-1] == '$'
		b.WriteString(rest[:i])
		if !escaped && config.OnlySecretReferences(token) {
			token = prefix + strconv.Itoa(len(placeholders)) + "x"
			placeholders = append(placeholders, token)
		}
		b.WriteString(token)
		rest = rest[i+end+1:]
	}
	return b.String(), placeholders
}
