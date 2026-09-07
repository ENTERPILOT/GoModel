package workflows

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
)

// Compiled workflows hold their plugin instances while live, so the
// guardrails service cannot close one a workflow still references.
func TestServiceInstallHoldsCompiledChains(t *testing.T) {
	s := &Service{}
	s.current.Store(newSnapshot())
	inst := &plugins.Instance{Name: "a"}
	compiled := &CompiledWorkflow{
		Version: Version{ID: "v1"},
		Policy:  &core.ResolvedWorkflowPolicy{},
		Chains:  &plugins.Chains{Prompt: &plugins.Chain{Steps: []plugins.Step{{Instances: []*plugins.Instance{inst}}}}},
	}
	next := newSnapshot()
	next.byScope[scopeRef{}] = compiled
	next.byVersionID["v1"] = compiled
	s.install(next)
	if !inst.Held() {
		t.Fatal("instance not held while its workflow is live")
	}

	// Re-installing the same compiled workflow (a snapshot clone) keeps one hold.
	s.install(cloneSnapshot(next))
	if !inst.Held() {
		t.Fatal("instance released by a snapshot that still carries its workflow")
	}

	s.install(newSnapshot())
	if inst.Held() {
		t.Fatal("instance still held after its workflow was dropped")
	}
}
