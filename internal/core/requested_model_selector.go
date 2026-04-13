package core

import "strings"

// RequestedModelSelector captures the raw selector as provided by a caller
// before alias resolution or provider routing.
type RequestedModelSelector struct {
	Model            string
	ProviderHint     string
	ExplicitProvider bool
}

// NewRequestedModelSelector normalizes raw selector input while preserving
// whether the provider came from an explicit field rather than model syntax.
func NewRequestedModelSelector(model, providerHint string) RequestedModelSelector {
	model = strings.TrimSpace(model)
	providerHint = strings.TrimSpace(providerHint)
	return RequestedModelSelector{
		Model:            model,
		ProviderHint:     providerHint,
		ExplicitProvider: providerHint != "",
	}
}

// Normalize returns the canonical routing selector for this request input.
func (s RequestedModelSelector) Normalize() (ModelSelector, error) {
	return ParseModelSelector(s.Model, s.ProviderHint)
}

// RequestedQualifiedModel returns the canonical requested selector string used
// for audit and workflow reporting.
func (s RequestedModelSelector) RequestedQualifiedModel() string {
	if !s.ExplicitProvider {
		return s.Model
	}
	if selector, err := s.Normalize(); err == nil {
		return selector.QualifiedModel()
	}
	if s.ProviderHint == "" {
		return s.Model
	}
	return s.ProviderHint + "/" + s.Model
}
