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
// This is intentionally small and pragmatic for the initial planning slice.
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

// ExecutionPlanSelector contains the request facts used to match one persisted
// execution-plan version.
type ExecutionPlanSelector struct {
	Provider string
	Model    string
}

// NewExecutionPlanSelector trims selector inputs for deterministic matching.
func NewExecutionPlanSelector(provider, model string) ExecutionPlanSelector {
	return ExecutionPlanSelector{
		Provider: strings.TrimSpace(provider),
		Model:    strings.TrimSpace(model),
	}
}

// ExecutionFeatures stores resolved per-request feature flags sourced from the
// matched persisted execution plan.
type ExecutionFeatures struct {
	Cache      bool
	Audit      bool
	Usage      bool
	Guardrails bool
}

// ApplyUpperBound returns features with process-level caps applied.
func (f ExecutionFeatures) ApplyUpperBound(caps ExecutionFeatures) ExecutionFeatures {
	return ExecutionFeatures{
		Cache:      f.Cache && caps.Cache,
		Audit:      f.Audit && caps.Audit,
		Usage:      f.Usage && caps.Usage,
		Guardrails: f.Guardrails && caps.Guardrails,
	}
}

// DefaultExecutionFeatures returns the permissive runtime default used when no
// persisted execution plan has been attached to the request.
func DefaultExecutionFeatures() ExecutionFeatures {
	return ExecutionFeatures{
		Cache:      true,
		Audit:      true,
		Usage:      true,
		Guardrails: true,
	}
}

// ResolvedExecutionPolicy is the request-scoped runtime projection of one
// matched persisted execution-plan version.
type ResolvedExecutionPolicy struct {
	VersionID      string
	Version        int
	ScopeProvider  string
	ScopeModel     string
	Name           string
	PlanHash       string
	Features       ExecutionFeatures
	GuardrailsHash string
}

// ExecutionPlan is the request-scoped control-plane result consumed by later
// execution stages. It carries the resolved execution mode, endpoint
// capabilities, and any model routing decision already made for the request.
type ExecutionPlan struct {
	RequestID    string
	Endpoint     EndpointDescriptor
	Mode         ExecutionMode
	Capabilities CapabilitySet
	ProviderType string
	Passthrough  *PassthroughRouteInfo
	Resolution   *RequestModelResolution
	Policy       *ResolvedExecutionPolicy
}

// RequestedQualifiedModel returns the requested model selector when present.
func (p *ExecutionPlan) RequestedQualifiedModel() string {
	if p == nil || p.Resolution == nil {
		return ""
	}
	return p.Resolution.RequestedQualifiedModel()
}

// ResolvedQualifiedModel returns the resolved model selector when present.
func (p *ExecutionPlan) ResolvedQualifiedModel() string {
	if p == nil || p.Resolution == nil {
		return ""
	}
	return p.Resolution.ResolvedQualifiedModel()
}

// ExecutionPlanVersionID returns the matched immutable execution-plan version id.
func (p *ExecutionPlan) ExecutionPlanVersionID() string {
	if p == nil || p.Policy == nil {
		return ""
	}
	return strings.TrimSpace(p.Policy.VersionID)
}

// CacheEnabled reports whether response caching is enabled for the request.
func (p *ExecutionPlan) CacheEnabled() bool {
	return p.featureEnabled(func(features ExecutionFeatures) bool { return features.Cache })
}

// AuditEnabled reports whether audit logging is enabled for the request.
func (p *ExecutionPlan) AuditEnabled() bool {
	return p.featureEnabled(func(features ExecutionFeatures) bool { return features.Audit })
}

// UsageEnabled reports whether usage tracking is enabled for the request.
func (p *ExecutionPlan) UsageEnabled() bool {
	return p.featureEnabled(func(features ExecutionFeatures) bool { return features.Usage })
}

// GuardrailsEnabled reports whether guardrail processing is enabled for the request.
func (p *ExecutionPlan) GuardrailsEnabled() bool {
	return p.featureEnabled(func(features ExecutionFeatures) bool { return features.Guardrails })
}

// GuardrailsHash returns the matched plan's guardrails hash.
func (p *ExecutionPlan) GuardrailsHash() string {
	if p == nil || p.Policy == nil || !p.GuardrailsEnabled() {
		return ""
	}
	return strings.TrimSpace(p.Policy.GuardrailsHash)
}

func (p *ExecutionPlan) featureEnabled(pick func(ExecutionFeatures) bool) bool {
	if p == nil || p.Policy == nil || strings.TrimSpace(p.Policy.VersionID) == "" {
		return pick(DefaultExecutionFeatures())
	}
	return pick(p.Policy.Features)
}
