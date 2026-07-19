package headerpolicy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

type registryEntry struct {
	policy core.HeaderPolicy
}

// Registry stores executable policies by reusable name.
type Registry struct {
	entries map[string]registryEntry
}

func newRegistry() *Registry {
	return &Registry{entries: make(map[string]registryEntry)}
}

func (r *Registry) register(def Definition) error {
	policy, err := NewPolicy(def)
	if err != nil {
		return err
	}
	if _, exists := r.entries[def.Name]; exists {
		return fmt.Errorf("duplicate header policy registration: %q", def.Name)
	}
	r.entries[def.Name] = registryEntry{policy: policy}
	return nil
}

// Names returns sorted policy names.
func (r *Registry) Names() []string {
	if r == nil {
		return []string{}
	}
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildHeaderPolicies resolves and orders workflow references.
func (r *Registry) BuildHeaderPolicies(steps []Reference) ([]core.HeaderPolicy, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	if r == nil {
		return nil, fmt.Errorf("header policy registry is required")
	}
	ordered := append([]Reference(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Step < ordered[j].Step })
	policies := make([]core.HeaderPolicy, 0, len(ordered))
	for _, step := range ordered {
		name := strings.TrimSpace(step.Ref)
		if name == "" {
			return nil, fmt.Errorf("header policy ref is required")
		}
		entry, ok := r.entries[name]
		if !ok {
			return nil, fmt.Errorf("unknown header policy ref: %q", name)
		}
		policies = append(policies, entry.policy)
	}
	return policies, nil
}
