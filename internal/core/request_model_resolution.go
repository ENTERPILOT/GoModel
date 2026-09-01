package core

import "strings"

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

	// resolvedQualified and resolvedRoute memoize the derived selector strings
	// (see CacheDerivedSelectors); empty means "compute on demand".
	resolvedQualified string
	resolvedRoute     string
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
	if r.resolvedQualified != "" {
		return r.resolvedQualified
	}
	return r.ResolvedSelector.QualifiedModel()
}

// ResolvedRouteModel returns the executed route selector recorded in audit
// logs: the resolved model qualified by the configured provider instance name
// when known, falling back to the selector's own provider prefix.
func (r *RequestModelResolution) ResolvedRouteModel() string {
	if r == nil {
		return ""
	}
	if r.resolvedRoute != "" {
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
