package core

import "strings"

// ExecutionMode describes how the gateway intends to execute a request.
type ExecutionMode string

const (
	ExecutionModeTranslated  ExecutionMode = "translated"
	ExecutionModePassthrough ExecutionMode = "passthrough"
	ExecutionModeNativeBatch ExecutionMode = "native_batch"
	ExecutionModeNativeFile  ExecutionMode = "native_file"
)

// CapabilitySet advertises the gateway behaviors that are valid for a request.
// This is intentionally small and pragmatic for the initial workflow slice.
type CapabilitySet struct {
	SemanticExtraction bool
	AliasResolution    bool
	Guardrails         bool
	RequestPatching    bool
	UsageTracking      bool
	ResponseCaching    bool
	Streaming          bool
	Passthrough        bool
}

// CapabilitiesForEndpoint returns the current capability set for one endpoint.
func CapabilitiesForEndpoint(desc EndpointDescriptor) CapabilitySet {
	switch desc.Operation {
	case OperationChatCompletions, OperationResponses:
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			Guardrails:         true,
			RequestPatching:    true,
			UsageTracking:      true,
			ResponseCaching:    true,
			Streaming:          true,
		}
	case OperationEmbeddings:
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			UsageTracking:      true,
			ResponseCaching:    true,
		}
	case OperationBatches:
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			Guardrails:         true,
			RequestPatching:    true,
			UsageTracking:      true,
		}
	case OperationFiles:
		return CapabilitySet{
			SemanticExtraction: true,
		}
	case OperationProviderPassthrough:
		return CapabilitySet{
			SemanticExtraction: true,
			Passthrough:        true,
		}
	default:
		return CapabilitySet{}
	}
}

// WorkflowSelector contains the request facts used to match one persisted
// workflow version.
type WorkflowSelector struct {
	// Provider is the configured provider instance name used for workflow matching.
	Provider string
	Model    string
	UserPath string
}

// NewWorkflowSelector trims selector inputs for deterministic matching.
func NewWorkflowSelector(provider, model string, userPath ...string) WorkflowSelector {
	selector := WorkflowSelector{
		Provider: strings.TrimSpace(provider),
		Model:    strings.TrimSpace(model),
	}
	if len(userPath) > 0 {
		if normalized, err := NormalizeUserPath(userPath[0]); err == nil {
			selector.UserPath = normalized
		}
	}
	return selector
}

// WorkflowFeatures stores resolved per-request feature flags sourced from the
// matched persisted workflow.
type WorkflowFeatures struct {
	Cache      bool `json:"cache"`
	Audit      bool `json:"audit"`
	Usage      bool `json:"usage"`
	Budget     bool `json:"budget"`
	Guardrails bool `json:"guardrails"`
	Fallback   bool `json:"fallback"`
}

// ApplyUpperBound returns features with process-level caps applied.
func (f WorkflowFeatures) ApplyUpperBound(caps WorkflowFeatures) WorkflowFeatures {
	return WorkflowFeatures{
		Cache:      f.Cache && caps.Cache,
		Audit:      f.Audit && caps.Audit,
		Usage:      f.Usage && caps.Usage,
		Budget:     f.Budget && caps.Budget,
		Guardrails: f.Guardrails && caps.Guardrails,
		Fallback:   f.Fallback && caps.Fallback,
	}
}

// DefaultWorkflowFeatures returns the permissive runtime default used when no
// persisted workflow has been attached to the request.
func DefaultWorkflowFeatures() WorkflowFeatures {
	return WorkflowFeatures{
		Cache:      true,
		Audit:      true,
		Usage:      true,
		Budget:     true,
		Guardrails: true,
		Fallback:   true,
	}
}

// ResolvedWorkflowPolicy is the request-scoped runtime projection of one
// matched persisted workflow version.
type ResolvedWorkflowPolicy struct {
	VersionID string
	Version   int
	// ScopeProvider is the configured provider instance name stored on the matched workflow.
	ScopeProvider  string
	ScopeModel     string
	ScopeUserPath  string
	Name           string
	WorkflowHash   string
	Features       WorkflowFeatures
	GuardrailsHash string
}

// Workflow is the request-scoped control-plane result consumed by later
// execution stages. It carries the resolved execution mode, endpoint
// capabilities, and any model routing decision already made for the request.
type Workflow struct {
	RequestID    string
	Endpoint     EndpointDescriptor
	Mode         ExecutionMode
	Capabilities CapabilitySet
	ProviderType string
	Passthrough  *PassthroughRouteInfo
	Resolution   *RequestModelResolution
	Policy       *ResolvedWorkflowPolicy
}

// RequestedQualifiedModel returns the requested model selector when present.
func (p *Workflow) RequestedQualifiedModel() string {
	if p == nil || p.Resolution == nil {
		return ""
	}
	return p.Resolution.RequestedQualifiedModel()
}

// ResolvedQualifiedModel returns the resolved model selector when present.
func (p *Workflow) ResolvedQualifiedModel() string {
	if p == nil || p.Resolution == nil {
		return ""
	}
	return p.Resolution.ResolvedQualifiedModel()
}

// WorkflowVersionID returns the matched immutable workflow version id.
func (p *Workflow) WorkflowVersionID() string {
	if p == nil || p.Policy == nil {
		return ""
	}
	return strings.TrimSpace(p.Policy.VersionID)
}

// CacheEnabled reports whether response caching is enabled for the request.
func (p *Workflow) CacheEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Cache })
}

// AuditEnabled reports whether audit logging is enabled for the request.
func (p *Workflow) AuditEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Audit })
}

// UsageEnabled reports whether usage tracking is enabled for the request.
func (p *Workflow) UsageEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Usage })
}

// BudgetEnabled reports whether budget checks are enabled for the request.
func (p *Workflow) BudgetEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Budget })
}

// GuardrailsEnabled reports whether guardrail processing is enabled for the request.
func (p *Workflow) GuardrailsEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Guardrails })
}

// FallbackEnabled reports whether translated-route fallback is enabled for the request.
func (p *Workflow) FallbackEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Fallback })
}

// GuardrailsHash returns the matched workflow's guardrails hash.
func (p *Workflow) GuardrailsHash() string {
	if p == nil || p.Policy == nil || !p.GuardrailsEnabled() {
		return ""
	}
	return strings.TrimSpace(p.Policy.GuardrailsHash)
}

func (p *Workflow) featureEnabled(pick func(WorkflowFeatures) bool) bool {
	if p == nil || p.Policy == nil || strings.TrimSpace(p.Policy.VersionID) == "" {
		return pick(DefaultWorkflowFeatures())
	}
	return pick(p.Policy.Features)
}
