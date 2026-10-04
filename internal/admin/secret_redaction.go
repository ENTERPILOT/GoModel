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
// swapped for placeholders first, since their braces are not valid in a URL.
func proxyPasswordIsReference(value string) bool {
	parsable, references := withReferencePlaceholders(value)
	u, err := url.Parse(parsable)
	if err != nil {
		return false
	}
	password, set := u.User.Password()
	if !set {
		return true
	}
	for placeholder, reference := range references {
		password = strings.ReplaceAll(password, placeholder, reference)
	}
	return config.OnlySecretReferences(password)
}

// withReferencePlaceholders replaces each secret reference in value with an
// alphanumeric placeholder and returns the placeholders' references.
func withReferencePlaceholders(value string) (string, map[string]string) {
	references := map[string]string{}
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
			placeholder := "gomodelsecretref" + strconv.Itoa(len(references)) + "x"
			references[placeholder] = token
			token = placeholder
		}
		b.WriteString(token)
		rest = rest[i+end+1:]
	}
	return b.String(), references
}
