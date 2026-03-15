package core

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
	case "chat_completions", "responses":
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			Guardrails:         true,
			RequestPatching:    true,
			UsageTracking:      true,
			ResponseCaching:    true,
			Streaming:          true,
		}
	case "embeddings":
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			UsageTracking:      true,
			ResponseCaching:    true,
		}
	case "batches":
		return CapabilitySet{
			SemanticExtraction: true,
			AliasResolution:    true,
			Guardrails:         true,
			RequestPatching:    true,
			UsageTracking:      true,
		}
	case "files":
		return CapabilitySet{
			SemanticExtraction: true,
		}
	case "provider_passthrough":
		return CapabilitySet{
			SemanticExtraction: true,
			Passthrough:        true,
		}
	default:
		return CapabilitySet{}
	}
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
	Resolution   *RequestModelResolution
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
