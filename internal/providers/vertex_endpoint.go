package providers

import (
	"net/url"
	"regexp"
	"strings"
)

// gcpRegionExpr is the shape of a Google Cloud region: "us-central1",
// "northamerica-northeast1".
const gcpRegionExpr = `[a-z]+-[a-z]+[0-9]+`

// vertexHostPattern matches the hosts Google serves Vertex AI on: the global
// endpoint aiplatform.googleapis.com, a regional {region}-aiplatform one, and
// aiplatform.{location}.rep.googleapis.com for a region or the us and eu
// multi-regions. It is deliberately narrower than "any googleapis.com host":
// Private Service Connect names under p.googleapis.com resolve through DNS the
// customer controls.
var vertexHostPattern = regexp.MustCompile(
	`^((` + gcpRegionExpr + `-)?aiplatform\.googleapis\.com|aiplatform\.(us|eu|` + gcpRegionExpr + `)\.rep\.googleapis\.com)$`)

// IsVertexEndpoint reports whether rawURL is an https URL on one of Google's
// own Vertex AI hosts and the default port. Adapters use it to decide whether
// an endpoint may receive a credential taken from the gateway's host rather
// than from the provider's own configuration (see ProviderConfig.Managed).
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
