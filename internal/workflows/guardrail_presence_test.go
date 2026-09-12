package workflows

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// chainCatalog is a guardrails.Catalog that returns one fixed prompt chain, so
// the service can be exercised with a workflow that runs guardrails without
// loading real plugins.
type chainCatalog struct{}

func (chainCatalog) Len() int        { return 1 }
func (chainCatalog) Names() []string { return []string{"stub"} }
func (chainCatalog) BuildChains([]guardrails.StepReference) (*plugins.Chains, error) {
	return &plugins.Chains{Prompt: &plugins.Chain{Phase: pluginapi.KindPrompt, Steps: []plugins.Step{{Order: 0}}}}, nil
}

// HasGuardrailChains tells a gateway that configures no guardrail at all from
// one where some workflow runs them: passthrough refuses a request whose model
// it could not read only in the second case.
func TestServiceHasGuardrailChains(t *testing.T) {
	global := Version{
		ID: "global", Scope: Scope{}, ScopeKey: "global", Version: 1, Active: true, Name: "global",
		Payload: Payload{SchemaVersion: 1, Features: FeatureFlags{Cache: true, Audit: true, Usage: true}},
	}
	guarded := Version{
		ID: "model", Scope: Scope{Provider: "openai", Model: "gpt-5"}, ScopeKey: "provider_model:openai:gpt-5",
		Version: 1, Active: true, Name: "guarded",
		Payload: Payload{SchemaVersion: 1, Features: FeatureFlags{Guardrails: true}, Steps: []Step{{Ref: "stub", Phase: "prompt", Step: 0}}},
	}

	for _, tt := range []struct {
		name     string
		versions []Version
		want     bool
	}{
		{name: "no guardrails", versions: []Version{global}},
		{name: "one guarded workflow", versions: []Version{global, guarded}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(&staticStore{versions: tt.versions}, NewCompilerWithFeatureCaps(chainCatalog{}, core.DefaultWorkflowFeatures()))
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			if err := service.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			if got := service.HasGuardrailChains(); got != tt.want {
				t.Fatalf("HasGuardrailChains() = %v, want %v", got, tt.want)
			}
		})
	}

	var nilService *Service
	if nilService.HasGuardrailChains() {
		t.Fatal("HasGuardrailChains() on a nil service = true, want false")
	}
}
