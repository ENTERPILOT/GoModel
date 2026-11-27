package core

import (
	"strings"
	"time"
)

// RequestModelResolution captures the requested model selector at ingress and
// the concrete selector chosen for execution after alias resolution.
type RequestModelResolution struct {
	Requested        RequestedModelSelector
	ResolvedSelector ModelSelector
	ProviderType     string
	ProviderName     string
	AliasApplied     bool
	// Slowdown is the extra-time factor selected for this request. A value of
	// 0.5 adds 50% of measured inference time; zero disables slowdown.
	Slowdown float64
	// Timeouts are the per-attempt upstream timeouts configured on the
	// virtual model or model policy that matched. Zero fields inherit the
	// gateway-wide HTTP timeouts.
	Timeouts UpstreamTimeouts
	// VirtualModelPath lists the virtual models routing passed through to
	// reach ResolvedSelector, outermost first. It is empty when no virtual
	// model applied.
	VirtualModelPath []string
	// RouteLevel is the level of the target a virtual model using the "auto"
	// strategy chose for this request. It is empty for every other request.
	RouteLevel string

	// resolvedQualified and resolvedRoute memoize the derived selector strings
	// (see CacheDerivedSelectors). cachedSelector and cachedProviderName record
	// the inputs they were derived from: a copy whose ResolvedSelector or
	// ProviderName is later rewritten (a failover clone, for instance) no
	// longer matches, so the getters recompute instead of serving stale values.
	resolvedQualified  string
	resolvedRoute      string
	cachedSelector     ModelSelector
	cachedProviderName string
}

// UpstreamTimeouts overrides the gateway-wide upstream HTTP timeouts for
// each attempt of one request, including retries and failovers, on paths
// that apply them (native Bedrock SDK calls and realtime handshakes do not).
// Each attempt gets a fresh budget. A zero field inherits the gateway-wide
// setting.
type UpstreamTimeouts struct {
	Timeout               time.Duration
	ResponseHeaderTimeout time.Duration
}

// CacheDerivedSelectors precomputes the strings returned by
// ResolvedQualifiedModel and ResolvedRouteModel, which are read several times
// per request. Call it once after the resolution is fully populated; the
// getters fall back to computing the values when it was not called.
func (r *RequestModelResolution) CacheDerivedSelectors() {
	if r == nil {
		return
	}
	r.resolvedQualified = r.ResolvedSelector.QualifiedModel()
	r.resolvedRoute = r.computeResolvedRouteModel()
	r.cachedSelector = r.ResolvedSelector
	r.cachedProviderName = r.ProviderName
}

// RequestedQualifiedModel returns the canonical requested selector.
func (r *RequestModelResolution) RequestedQualifiedModel() string {
	if r == nil {
		return ""
	}
	return r.Requested.RequestedQualifiedModel()
}

// ResolvedQualifiedModel returns the concrete qualified model selected for execution.
func (r *RequestModelResolution) ResolvedQualifiedModel() string {
	if r == nil {
		return ""
	}
	if r.resolvedQualified != "" && r.cachedSelector == r.ResolvedSelector {
		return r.resolvedQualified
	}
	return r.ResolvedSelector.QualifiedModel()
}

// ResolvedRouteModel returns the executed route selector recorded in request
// logs: the resolved model qualified by the configured provider instance name
// when known, falling back to the selector's own provider prefix.
func (r *RequestModelResolution) ResolvedRouteModel() string {
	if r == nil {
		return ""
	}
	if r.resolvedRoute != "" && r.cachedSelector == r.ResolvedSelector && r.cachedProviderName == r.ProviderName {
		return r.resolvedRoute
	}
	return r.computeResolvedRouteModel()
}

func (r *RequestModelResolution) computeResolvedRouteModel() string {
	model := strings.TrimSpace(r.ResolvedSelector.Model)
	if model == "" {
		return ""
	}
	if providerName := strings.TrimSpace(r.ProviderName); providerName != "" {
		return providerName + "/" + model
	}
	if provider := strings.TrimSpace(r.ResolvedSelector.Provider); provider != "" {
		return provider + "/" + model
	}
	return model
}
