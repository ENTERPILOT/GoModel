package core

// RequestModelResolution captures the requested model selector at ingress and
// the concrete selector chosen for execution after alias resolution.
type RequestModelResolution struct {
	Requested        RequestedModelSelector
	ResolvedSelector ModelSelector
	ProviderType     string
	AliasApplied     bool
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
	return r.ResolvedSelector.QualifiedModel()
}
