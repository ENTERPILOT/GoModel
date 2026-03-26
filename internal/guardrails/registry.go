package guardrails

import (
	"fmt"
	"strings"

	"gomodel/internal/responsecache"
)

// StepReference points to one named guardrail and the step it should run at.
type StepReference struct {
	Ref  string
	Step int
}

type registryEntry struct {
	guardrail  Guardrail
	descriptor responsecache.GuardrailRuleDescriptor
}

// Registry stores named guardrails so execution plans can reference them by id.
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

// Register adds one named guardrail and its hashing descriptor.
func (r *Registry) Register(g Guardrail, descriptor responsecache.GuardrailRuleDescriptor) error {
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

// BuildPipeline resolves named guardrail references into an executable pipeline and hash.
func (r *Registry) BuildPipeline(steps []StepReference) (*Pipeline, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}
	if r == nil {
		return nil, "", fmt.Errorf("guardrail registry is required")
	}

	pipeline := NewPipeline()
	descriptors := make([]responsecache.GuardrailRuleDescriptor, 0, len(steps))
	for _, step := range steps {
		name := strings.TrimSpace(step.Ref)
		if name == "" {
			return nil, "", fmt.Errorf("guardrail ref is required")
		}
		entry, ok := r.entries[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown guardrail ref: %q", name)
		}
		pipeline.Add(entry.guardrail, step.Step)
		descriptor := entry.descriptor
		descriptor.Order = step.Step
		descriptors = append(descriptors, descriptor)
	}
	return pipeline, responsecache.ComputeGuardrailsHash(descriptors), nil
}
