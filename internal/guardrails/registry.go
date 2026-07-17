package guardrails

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// StepReference points to one named guardrail and the step it should run at.
type StepReference struct {
	Ref  string
	Step int
}

type registryEntry struct {
	guardrail    Guardrail
	headerPolicy core.HeaderPolicy
	descriptor   RuleDescriptor
}

// GuardrailNames returns only message-processing definitions.
func (r *Registry) GuardrailNames() []string {
	return r.namesMatching(func(entry registryEntry) bool { return entry.guardrail != nil })
}

// HeaderPolicyNames returns only outbound header-policy definitions.
func (r *Registry) HeaderPolicyNames() []string {
	return r.namesMatching(func(entry registryEntry) bool { return entry.headerPolicy != nil })
}

func (r *Registry) namesMatching(matches func(registryEntry) bool) []string {
	if r == nil {
		return []string{}
	}
	names := make([]string, 0, len(r.entries))
	for name, entry := range r.entries {
		if matches(entry) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Registry stores named guardrails so workflows can reference them by id.
type Registry struct {
	entries map[string]registryEntry
}

// NewRegistry creates an empty named guardrail registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]registryEntry)}
}

// Len returns the number of registered named guardrails.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.entries)
}

// Names returns the registered guardrail names in sorted order.
func (r *Registry) Names() []string {
	if r == nil || len(r.entries) == 0 {
		return []string{}
	}

	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Register adds one named guardrail and its hashing descriptor.
func (r *Registry) Register(g Guardrail, descriptor RuleDescriptor) error {
	if r == nil {
		return fmt.Errorf("registry is required")
	}
	if g == nil {
		return fmt.Errorf("guardrail is required")
	}
	name := strings.TrimSpace(g.Name())
	if name == "" {
		return fmt.Errorf("guardrail name is required")
	}
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("duplicate guardrail registration: %q", name)
	}
	descriptor.Name = name
	r.entries[name] = registryEntry{
		guardrail:  g,
		descriptor: descriptor,
	}
	return nil
}

// RegisterHeaderPolicy adds one named outbound-attempt policy.
func (r *Registry) RegisterHeaderPolicy(policy core.HeaderPolicy, descriptor RuleDescriptor) error {
	if r == nil {
		return fmt.Errorf("registry is required")
	}
	if policy == nil {
		return fmt.Errorf("header policy is required")
	}
	name := strings.TrimSpace(policy.Name())
	if name == "" {
		return fmt.Errorf("header policy name is required")
	}
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("duplicate policy registration: %q", name)
	}
	descriptor.Name = name
	r.entries[name] = registryEntry{headerPolicy: policy, descriptor: descriptor}
	return nil
}

// BuildPipeline resolves named guardrail references into an executable pipeline and hash.
func (r *Registry) BuildPipeline(steps []StepReference) (*Pipeline, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}
	if r == nil {
		return nil, "", fmt.Errorf("guardrail registry is required")
	}

	pipeline := NewPipeline()
	descriptors := make([]RuleDescriptor, 0, len(steps))
	for _, step := range steps {
		name := strings.TrimSpace(step.Ref)
		if name == "" {
			return nil, "", fmt.Errorf("guardrail ref is required")
		}
		entry, ok := r.entries[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown guardrail ref: %q", name)
		}
		if entry.guardrail == nil {
			return nil, "", fmt.Errorf("ref %q is an outbound header policy, not a guardrail", name)
		}
		pipeline.Add(entry.guardrail, step.Step)
		descriptor := entry.descriptor
		descriptor.Order = step.Step
		descriptors = append(descriptors, descriptor)
	}
	return pipeline, ComputeGuardrailsHash(descriptors), nil
}

// BuildHeaderPolicies resolves named references into ordered egress policies
// and a deterministic configuration hash.
func (r *Registry) BuildHeaderPolicies(steps []StepReference) ([]core.HeaderPolicy, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}
	if r == nil {
		return nil, "", fmt.Errorf("policy registry is required")
	}
	ordered := append([]StepReference(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Step < ordered[j].Step })
	policies := make([]core.HeaderPolicy, 0, len(ordered))
	descriptors := make([]RuleDescriptor, 0, len(ordered))
	for _, step := range ordered {
		name := strings.TrimSpace(step.Ref)
		if name == "" {
			return nil, "", fmt.Errorf("header policy ref is required")
		}
		entry, ok := r.entries[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown header policy ref: %q", name)
		}
		if entry.headerPolicy == nil {
			return nil, "", fmt.Errorf("ref %q is a guardrail, not an outbound header policy", name)
		}
		policies = append(policies, entry.headerPolicy)
		descriptor := entry.descriptor
		descriptor.Order = step.Step
		descriptors = append(descriptors, descriptor)
	}
	return policies, ComputeGuardrailsHash(descriptors), nil
}
