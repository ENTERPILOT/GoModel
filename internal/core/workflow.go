package core

import (
	"strings"

	"github.com/goccy/go-json"
)

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
	case OperationSystemOne, OperationDecisions:
		return CapabilitySet{
			AliasResolution: true,
			Guardrails:      true,
			UsageTracking:   true,
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
	Cache       bool `json:"cache"`
	RequestLogs bool `json:"request_logs"`
	Usage       bool `json:"usage"`
	Budget      bool `json:"budget"`
	Guardrails  bool `json:"guardrails"`
	Failover    bool `json:"failover"`
}

// workflowFeaturesWire is WorkflowFeatures as JSON. The request_logs flag was
// named audit through v0.2.5, and clients and replicas of that version read
// only audit, so both keys are written. request_logs wins when both are read.
type workflowFeaturesWire struct {
	Cache       bool  `json:"cache"`
	RequestLogs *bool `json:"request_logs,omitempty"`
	Audit       *bool `json:"audit,omitempty"`
	Usage       bool  `json:"usage"`
	Budget      bool  `json:"budget"`
	Guardrails  bool  `json:"guardrails"`
	Failover    bool  `json:"failover"`
}

// MarshalJSON writes request_logs and its deprecated alias audit.
func (f WorkflowFeatures) MarshalJSON() ([]byte, error) {
	return json.Marshal(workflowFeaturesWire{
		Cache:       f.Cache,
		RequestLogs: &f.RequestLogs,
		Audit:       &f.RequestLogs, // deprecated alias: drop this line to stop writing it
		Usage:       f.Usage,
		Budget:      f.Budget,
		Guardrails:  f.Guardrails,
		Failover:    f.Failover,
	})
}

// UnmarshalJSON reads request_logs, falling back to its deprecated alias audit.
func (f *WorkflowFeatures) UnmarshalJSON(data []byte) error {
	var wire workflowFeaturesWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*f = WorkflowFeatures{
		Cache:       wire.Cache,
		RequestLogs: RequestLogsFlag(wire.RequestLogs, wire.Audit),
		Usage:       wire.Usage,
		Budget:      wire.Budget,
		Guardrails:  wire.Guardrails,
		Failover:    wire.Failover,
	}
	return nil
}

// RequestLogsFlag resolves the workflow request_logs flag from a decoded
// document: requestLogs when the document carried it, otherwise its deprecated
// alias audit, otherwise false.
func RequestLogsFlag(requestLogs, audit *bool) bool {
	switch {
	case requestLogs != nil:
		return *requestLogs
	case audit != nil:
		return *audit
	default:
		return false
	}
}

// ApplyUpperBound returns features with process-level caps applied.
func (f WorkflowFeatures) ApplyUpperBound(caps WorkflowFeatures) WorkflowFeatures {
	usage := f.Usage && caps.Usage
	return WorkflowFeatures{
		Cache:       f.Cache && caps.Cache,
		RequestLogs: f.RequestLogs && caps.RequestLogs,
		Usage:       usage,
		Budget:      usage && f.Budget && caps.Budget,
		Guardrails:  f.Guardrails && caps.Guardrails,
		Failover:    f.Failover && caps.Failover,
	}
}

// DefaultWorkflowFeatures returns the permissive runtime default used when no
// persisted workflow has been attached to the request.
func DefaultWorkflowFeatures() WorkflowFeatures {
	return WorkflowFeatures{
		Cache:       true,
		RequestLogs: true,
		Usage:       true,
		Budget:      true,
		Guardrails:  true,
		Failover:    true,
	}
}

// ResolvedWorkflowPolicy is the request-scoped runtime projection of one
// matched persisted workflow version.
type ResolvedWorkflowPolicy struct {
	VersionID string
	Version   int
	// ScopeProvider is the configured provider instance name stored on the matched workflow.
	ScopeProvider string
	ScopeModel    string
	ScopeUserPath string
	Name          string
	WorkflowHash  string
	Features      WorkflowFeatures
	// GuardrailsHash is the prompt-phase plugin chain hash; it feeds the
	// response cache key.
	GuardrailsHash string
	// ChainHashes holds the per-phase plugin chain hashes ("prompt",
	// "response", "stream"), for diagnostics and the admin views.
	ChainHashes map[string]string
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
	// PluginState is the per-request plugin state, created by the plugin
	// runtime the first time a plugin runs for the request and nil otherwise.
	// It lives here rather than in the context so a request that runs no
	// plugin allocates nothing.
	PluginState any
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

// RequestLogEnabled reports whether request logging is enabled for the request.
func (p *Workflow) RequestLogEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.RequestLogs })
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

// FailoverEnabled reports whether translated-route failover is enabled for the request.
func (p *Workflow) FailoverEnabled() bool {
	return p.featureEnabled(func(features WorkflowFeatures) bool { return features.Failover })
}

// GuardrailsHash returns the matched workflow's guardrails hash.
func (p *Workflow) GuardrailsHash() string {
	if p == nil || p.Policy == nil || !p.GuardrailsEnabled() {
		return ""
	}
	return strings.TrimSpace(p.Policy.GuardrailsHash)
}

// PromptGuardrailsHash returns the hash of the guardrails that run on the
// prompt, or "" when none do (no guardrails, guardrails disabled, or only
// response- and stream-phase rules).
func (p *Workflow) PromptGuardrailsHash() string {
	if p == nil || p.Policy == nil || !p.GuardrailsEnabled() {
		return ""
	}
	return strings.TrimSpace(p.Policy.ChainHashes["prompt"])
}

func (p *Workflow) featureEnabled(pick func(WorkflowFeatures) bool) bool {
	if p == nil || p.Policy == nil || strings.TrimSpace(p.Policy.VersionID) == "" {
		return pick(DefaultWorkflowFeatures())
	}
	return pick(p.Policy.Features)
}
