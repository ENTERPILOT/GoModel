package providers

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// PassthroughEndpoint normalizes a provider-relative passthrough endpoint into
// an absolute path fragment suitable for baseURL + endpoint request building.
func PassthroughEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "/"
	}
	if strings.HasPrefix(endpoint, "/") {
		return endpoint
	}
	return "/" + endpoint
}

// CloneHTTPHeaders returns a detached copy of an http.Header map.
func CloneHTTPHeaders(src http.Header) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for key, values := range src {
		cloned := make([]string, len(values))
		copy(cloned, values)
		dst[key] = cloned
	}
	return dst
}

// PassthroughEndpointPath returns the normalized path portion of a provider
// passthrough endpoint, preferring a semantic normalized endpoint when present.
func PassthroughEndpointPath(info *core.PassthroughRouteInfo) string {
	if info == nil {
		return ""
	}
	endpoint := strings.TrimSpace(info.NormalizedEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(info.RawEndpoint)
	}
	endpoint, _, _ = strings.Cut(endpoint, "?")
	if endpoint == "" {
		return ""
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return endpoint
}

// PassthroughEndpointSemantics names the semantic operation and audit path
// for one provider passthrough endpoint.
type PassthroughEndpointSemantics struct {
	Operation string
	AuditPath string
}

// SemanticEnricher implements core.PassthroughSemanticEnricher from a static
// endpoint table. Endpoints missing from the table keep their audit path, or
// fall back to the generic /p/{provider}/... form when none is set.
type SemanticEnricher struct {
	providerType string
	endpoints    map[string]PassthroughEndpointSemantics
}

// NewSemanticEnricher builds a SemanticEnricher for a provider type from its
// endpoint table; keys are normalized endpoint paths such as "/embeddings".
func NewSemanticEnricher(providerType string, endpoints map[string]PassthroughEndpointSemantics) SemanticEnricher {
	return SemanticEnricher{providerType: providerType, endpoints: endpoints}
}

// ProviderType returns the provider type this enricher serves.
func (e SemanticEnricher) ProviderType() string {
	return e.providerType
}

// Enrich annotates passthrough route info with the provider's semantic
// operation and audit path for known endpoints.
func (e SemanticEnricher) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	enriched := *info
	normalizedEndpoint := strings.TrimLeft(strings.TrimSpace(PassthroughEndpointPath(&enriched)), "/")
	if semantics, ok := e.endpoints["/"+normalizedEndpoint]; ok {
		enriched.SemanticOperation = semantics.Operation
		enriched.AuditPath = semantics.AuditPath
	} else if strings.TrimSpace(enriched.AuditPath) == "" && normalizedEndpoint != "" {
		enriched.AuditPath = "/p/" + e.providerType + "/" + normalizedEndpoint
	}
	return &enriched
}
