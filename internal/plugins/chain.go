package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Ref points a chain at an instance running at a step.
type Ref struct {
	Instance *Instance
	Step     int
}

// Step groups the instances that run together at one order value.
type Step struct {
	Order     int
	Instances []*Instance
}

// Chain is the ordered set of instances of one phase.
type Chain struct {
	Phase pluginapi.Kind
	Steps []Step
	Hash  string
}

// BuildChain groups refs by step and validates them: every instance must
// implement the phase hook and a step may hold at most one mutating instance
// (non-mutating instances of a step run concurrently).
func BuildChain(phase pluginapi.Kind, refs []Ref) (*Chain, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if !IsPhaseKind(phase) {
		return nil, fmt.Errorf("plugins: %q is not a chain phase", phase)
	}
	byStep := map[int][]*Instance{}
	seen := map[string]struct{}{}
	descriptors := make([]RuleDescriptor, 0, len(refs))
	for _, ref := range refs {
		inst := ref.Instance
		if inst == nil {
			return nil, fmt.Errorf("plugins: %s chain has a nil instance", phase)
		}
		if _, dup := seen[inst.Name]; dup {
			return nil, fmt.Errorf("plugins: instance %q appears twice in the %s chain", inst.Name, phase)
		}
		seen[inst.Name] = struct{}{}
		if !inst.HasKind(phase) {
			return nil, fmt.Errorf("plugins: instance %q (%s) does not implement the %s hook", inst.Name, inst.Type, phase)
		}
		byStep[ref.Step] = append(byStep[ref.Step], inst)
		descriptors = append(descriptors, RuleDescriptor{
			Name:    inst.Name,
			Type:    inst.Type,
			Order:   ref.Step,
			Mode:    string(inst.EffectiveFailMode(phase)),
			Content: inst.ConfigHash,
		})
	}
	orders := make([]int, 0, len(byStep))
	for order := range byStep {
		orders = append(orders, order)
	}
	sort.Ints(orders)
	chain := &Chain{Phase: phase, Hash: ComputeChainHash(descriptors)}
	for _, order := range orders {
		instances := byStep[order]
		mutating := 0
		for _, inst := range instances {
			if inst.Mutates() {
				mutating++
			}
		}
		if mutating > 1 {
			return nil, fmt.Errorf("plugins: %s step %d has %d mutating instances; only one mutating plugin may share a step", phase, order, mutating)
		}
		chain.Steps = append(chain.Steps, Step{Order: order, Instances: instances})
	}
	return chain, nil
}

// Empty reports whether the chain has no instances.
func (c *Chain) Empty() bool {
	return c == nil || len(c.Steps) == 0
}

// Len returns the number of instances.
func (c *Chain) Len() int {
	if c == nil {
		return 0
	}
	n := 0
	for _, step := range c.Steps {
		n += len(step.Instances)
	}
	return n
}

// StepsOf returns the steps of a possibly nil chain.
func (c *Chain) StepsOf() []Step {
	if c == nil {
		return nil
	}
	return c.Steps
}

// Instances lists the instances in step order.
func (c *Chain) Instances() []*Instance {
	if c == nil {
		return nil
	}
	out := make([]*Instance, 0, c.Len())
	for _, step := range c.Steps {
		out = append(out, step.Instances...)
	}
	return out
}

// Chains holds the compiled chain of every phase of one workflow.
type Chains struct {
	Prompt   *Chain
	Response *Chain
	Stream   *Chain
}

// Hashes returns the non-empty chain hashes keyed by phase.
func (c *Chains) Hashes() map[string]string {
	if c == nil {
		return nil
	}
	hashes := map[string]string{}
	for phase, chain := range map[pluginapi.Kind]*Chain{
		pluginapi.KindPrompt:   c.Prompt,
		pluginapi.KindResponse: c.Response,
		pluginapi.KindStream:   c.Stream,
	} {
		if !chain.Empty() {
			hashes[string(phase)] = chain.Hash
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	return hashes
}

// Empty reports whether no phase has a chain.
func (c *Chains) Empty() bool {
	return c == nil || (c.Prompt.Empty() && c.Response.Empty() && c.Stream.Empty())
}

// PromptHash returns the prompt chain hash.
func (c *Chains) PromptHash() string {
	if c == nil || c.Prompt.Empty() {
		return ""
	}
	return c.Prompt.Hash
}

// CacheHash returns the hash the response cache keys on: the prompt chain
// hash alone when no response or stream chain exists (so keys of existing
// prompt-only workflows are unchanged), otherwise a digest of every phase
// hash, because cached bodies were produced by the response and stream
// chains too.
func (c *Chains) CacheHash() string {
	if c == nil || (c.Response.Empty() && c.Stream.Empty()) {
		return c.PromptHash()
	}
	h := sha256.Sum256([]byte(c.PromptHash() + "|" + c.Response.hashOf() + "|" + c.Stream.hashOf()))
	return hex.EncodeToString(h[:])
}

func (c *Chain) hashOf() string {
	if c.Empty() {
		return ""
	}
	return c.Hash
}
