package workflows

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// offeredStep identifies one on-request step: a guardrail in one phase.
type offeredStep struct {
	phase pluginapi.Kind
	ref   string
}

// offer records step as one a request may add by naming its guardrail.
func (c *CompiledWorkflow) offer(step Step) {
	phase := pluginapi.Kind(step.Phase)
	if phase == "" {
		phase = pluginapi.KindPrompt
	}
	if c.onRequest == nil {
		c.onRequest = map[offeredStep]bool{}
		c.offered = map[string]bool{}
	}
	ref := strings.TrimSpace(step.Ref)
	c.onRequest[offeredStep{phase: phase, ref: ref}] = true
	c.offered[ref] = true
}

// runsAlways reports whether the workflow runs the guardrail on every request.
func (c *CompiledWorkflow) runsAlways(name string) bool {
	if c.Chains == nil {
		return false
	}
	for _, chain := range []*plugins.Chain{c.Chains.Prompt, c.Chains.Response, c.Chains.Stream} {
		for _, inst := range chain.Instances() {
			if inst.Name == name {
				return true
			}
		}
	}
	return false
}

// requestChains returns the chains for a request that names guardrails: the
// workflow's fixed steps plus the on-request steps of the named guardrails.
// added lists the names that added a step; a name the workflow runs on every
// request anyway adds nothing. A name the workflow neither runs nor offers is
// an error, so a requested check is never skipped silently.
func (c *CompiledWorkflow) requestChains(names []string) (chains *plugins.Chains, added []string, err error) {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		switch {
		case c.offered[name]:
			selected[name] = true
			added = append(added, name)
		case c.runsAlways(name):
		default:
			return nil, nil, core.NewRequestGuardrailUnavailableError(name)
		}
	}
	if len(added) == 0 {
		return c.Chains, nil, nil
	}
	chains, err = c.Offered.Filter(func(phase pluginapi.Kind, instance string) bool {
		return !c.onRequest[offeredStep{phase: phase, ref: instance}] || selected[instance]
	})
	if err != nil {
		return nil, nil, core.NewProviderError("", http.StatusInternalServerError, "failed to build the request guardrail chain", err)
	}
	return chains, added, nil
}

// SelectRequestGuardrails adds the guardrails a request named in its
// guardrails field to the request's workflow. Only steps the matched workflow
// marks on_request can be added; its other steps run regardless and cannot be
// removed. The workflow's policy hashes are replaced with those of the
// resulting chains, so the response cache keys on what actually runs, and
// ChainsForWorkflow serves those chains to every phase of the request.
func (s *Service) SelectRequestGuardrails(workflow *core.Workflow, names []string) error {
	if len(names) == 0 {
		return nil
	}
	compiled := s.compiledForWorkflow(workflow)
	if compiled == nil {
		return core.NewRequestGuardrailUnavailableError(names[0])
	}
	chains, added, err := compiled.requestChains(names)
	if err != nil || len(added) == 0 {
		return err
	}
	policy := *workflow.Policy
	policy.GuardrailsHash = chains.CacheHash()
	policy.ChainHashes = chains.Hashes()
	workflow.Policy = &policy
	workflow.RequestGuardrails = added
	return nil
}

// compiledForWorkflow returns the live compiled workflow a request matched,
// or nil when it matched none or guardrails are off for it.
func (s *Service) compiledForWorkflow(workflow *core.Workflow) *CompiledWorkflow {
	if s == nil || workflow == nil || workflow.Policy == nil || !workflow.GuardrailsEnabled() {
		return nil
	}
	versionID := strings.TrimSpace(workflow.Policy.VersionID)
	if versionID == "" {
		return nil
	}
	return s.snapshot().byVersionID[versionID]
}
