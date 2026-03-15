package core

// RequestModelResolution captures the requested model selector at ingress and
// the concrete selector chosen for execution after alias resolution.
type RequestModelResolution struct {
	RequestedModel    string
	RequestedProvider string
	ResolvedSelector  ModelSelector
	ProviderType      string
	AliasApplied      bool
}

// RequestedQualifiedModel reconstructs the raw requested selector.
func (r *RequestModelResolution) RequestedQualifiedModel() string {
	if r == nil {
		return ""
	}
	if r.RequestedProvider == "" {
		return r.RequestedModel
	}
	if selector, err := ParseModelSelector(r.RequestedModel, r.RequestedProvider); err == nil {
		return selector.QualifiedModel()
	}
	return r.RequestedProvider + "/" + r.RequestedModel
}

// ResolvedQualifiedModel returns the concrete qualified model selected for execution.
func (r *RequestModelResolution) ResolvedQualifiedModel() string {
	if r == nil {
		return ""
	}
	return r.ResolvedSelector.QualifiedModel()
}
