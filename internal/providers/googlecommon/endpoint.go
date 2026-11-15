package googlecommon

import (
	"net/url"
	"regexp"
	"strings"
)

// locationExpr is the shape of a Google Cloud location: a region such as
// "us-central1" or "northamerica-northeast1", or a multi-region such as "us".
const locationExpr = `[a-z]+(-[a-z]+[0-9]+)?`

// vertexHostPattern matches the hosts Google serves Vertex AI on: the global
// endpoint, a regional one, and a regional or multi-region endpoint under
// rep.googleapis.com. It is deliberately narrower than "any googleapis.com
// host": Private Service Connect names under p.googleapis.com resolve through
// DNS the customer controls.
var vertexHostPattern = regexp.MustCompile(
	`^((` + locationExpr + `-)?aiplatform\.googleapis\.com|aiplatform\.` + locationExpr + `\.rep\.googleapis\.com)$`)

// IsVertexEndpoint reports whether rawURL is an https URL on one of Google's
// own Vertex AI hosts and the default port. Adapters use it to decide whether
// an endpoint may receive a credential taken from the gateway's host rather
// than from the provider's own configuration.
func IsVertexEndpoint(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return vertexHostPattern.MatchString(host)
}

// UsesHostCredentials reports whether cfg authenticates with a credential
// taken from the gateway's host: Application Default Credentials, or a
// service-account key read from a file on the host. Only service-account JSON
// carried in the configuration itself belongs to the provider.
func UsesHostCredentials(cfg Config) bool {
	if NormalizeAuthType(cfg.AuthType, HasServiceAccount(cfg)) == "gcp_adc" {
		return true
	}
	return strings.TrimSpace(cfg.ServiceAccountJSONBase64) == "" &&
		strings.TrimSpace(cfg.ServiceAccountJSON) == ""
}
