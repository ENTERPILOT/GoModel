package workflows

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
)

func systemPromptGuardrail(name string) guardrails.Definition {
	return guardrails.Definition{Name: name, Type: "system_prompt", Config: []byte(`{"mode":"inject","content":"be precise"}`)}
}

func TestCompilerCompile_Guardrails(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("policy-system"))

	compiled, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 3,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
			Guardrails: []GuardrailStep{
				{Ref: "policy-system", Step: 20},
			},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if compiled == nil || compiled.Chains == nil {
		t.Fatal("Compile() returned nil chains")
	}
	if compiled.Chains.Prompt.Len() != 1 || !compiled.Chains.Response.Empty() {
		t.Fatalf("chains = %+v, want one prompt step from the v1 payload", compiled.Chains)
	}
	if compiled.Policy == nil || compiled.Policy.GuardrailsHash == "" {
		t.Fatal("compiled guardrails hash is empty")
	}
	if compiled.Policy.ChainHashes["prompt"] != compiled.Policy.GuardrailsHash || len(compiled.Policy.ChainHashes) != 1 {
		t.Fatalf("chain hashes = %v", compiled.Policy.ChainHashes)
	}
}

func TestCompilerCompile_PhasesFromV2Steps(t *testing.T) {
	registry := newGuardrailService(t, guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{}, nil
	}), guardrails.Definition{Name: "privacy", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o-mini"}`)})

	compiled, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-2", Name: "global",
		Payload: Payload{
			SchemaVersion: 2,
			Features:      FeatureFlags{Guardrails: true},
			Steps: []Step{
				{Ref: "privacy", Phase: PhasePrompt, Step: 10},
				{Ref: "privacy", Phase: PhaseResponse, Step: 10},
			},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if compiled.Chains.Prompt.Len() != 1 || compiled.Chains.Response.Len() != 1 || !compiled.Chains.Stream.Empty() {
		t.Fatalf("chains = %+v", compiled.Chains)
	}
	if len(compiled.Policy.ChainHashes) != 2 {
		t.Fatalf("chain hashes = %v", compiled.Policy.ChainHashes)
	}

	_, err = NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-3", Name: "global",
		Payload: Payload{
			SchemaVersion: 2,
			Features:      FeatureFlags{Guardrails: true},
			Steps:         []Step{{Ref: "privacy", Phase: PhaseStream, Step: 10}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support the stream phase") {
		t.Fatalf("Compile(stream) error = %v, want unsupported phase", err)
	}
}

func TestCompilerCompile_AppliesProcessFeatureCaps(t *testing.T) {
	failoverEnabled := true
	compiled, err := NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      false,
		Guardrails: false,
		Failover:   false,
	}).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true, Failover: &failoverEnabled},
			Guardrails: []GuardrailStep{
				{Ref: "policy-system", Step: 10},
			},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if compiled == nil || compiled.Policy == nil {
		t.Fatal("Compile() returned nil policy")
	}
	if compiled.Policy.Features.Cache {
		t.Fatal("Policy.Features.Cache = true, want false")
	}
	if !compiled.Policy.Features.Audit {
		t.Fatal("Policy.Features.Audit = false, want true")
	}
	if compiled.Policy.Features.Usage {
		t.Fatal("Policy.Features.Usage = true, want false")
	}
	if compiled.Policy.Features.Guardrails {
		t.Fatal("Policy.Features.Guardrails = true, want false")
	}
	if compiled.Policy.Features.Failover {
		t.Fatal("Policy.Features.Failover = true, want false")
	}
	if compiled.Chains != nil {
		t.Fatal("compiled chains are not nil")
	}
	if compiled.Policy.GuardrailsHash != "" || compiled.Policy.ChainHashes != nil {
		t.Fatalf("compiled guardrails hash = %q / %v, want empty", compiled.Policy.GuardrailsHash, compiled.Policy.ChainHashes)
	}
}

func TestCompilerCompile_DefaultsFailoverEnabledWhenUnset(t *testing.T) {
	compiled, err := NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if !compiled.Policy.Features.Failover {
		t.Fatal("Policy.Features.Failover = false, want true by default")
	}
}

func TestCompilerCompile_RejectsGuardrailsWithoutRegistry(t *testing.T) {
	_, err := NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "policy-system", Step: 10}},
		},
	})
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != 502 {
		t.Fatalf("Compile() error = %v, want 502 gateway error", err)
	}
	_, err = NewCompilerWithFeatureCaps(newGuardrailService(t, nil), core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "policy-system", Step: 10}},
		},
	})
	if !errors.As(err, &gatewayErr) || !strings.Contains(err.Error(), "no guardrails are loaded") {
		t.Fatalf("Compile() with empty registry error = %v", err)
	}
}

func TestCompilerCompile_WrapsBuildChainsErrorsAsGatewayErrors(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("present"))
	_, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "missing", Step: 10}},
		},
	})
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != 502 || !strings.Contains(err.Error(), "unknown guardrail ref") {
		t.Fatalf("Compile() error = %v, want wrapped unknown ref error", err)
	}
}
