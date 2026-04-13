package workflows

import (
	"errors"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/responsecache"
)

func TestCompilerCompile_Guardrails(t *testing.T) {
	registry := guardrails.NewRegistry()
	rule, err := guardrails.NewSystemPromptGuardrail("policy-system", guardrails.SystemPromptInject, "be precise")
	if err != nil {
		t.Fatalf("NewSystemPromptGuardrail() error = %v", err)
	}
	if err := registry.Register(rule, responsecache.GuardrailRuleDescriptor{
		Type:    "system_prompt",
		Mode:    string(guardrails.SystemPromptInject),
		Content: "be precise",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	compiled, err := NewCompiler(registry).Compile(Version{
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
	if compiled == nil {
		t.Fatal("Compile() returned nil")
	}
	if compiled.Pipeline == nil {
		t.Fatal("compiled pipeline is nil")
	}
	if compiled.Pipeline.Len() != 1 {
		t.Fatalf("compiled pipeline len = %d, want 1", compiled.Pipeline.Len())
	}
	if compiled.Policy == nil {
		t.Fatal("compiled policy is nil")
	}
	if compiled.Policy.GuardrailsHash == "" {
		t.Fatal("compiled guardrails hash is empty")
	}
}

func TestCompilerCompile_AppliesProcessFeatureCaps(t *testing.T) {
	fallbackEnabled := true
	compiled, err := NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      false,
		Guardrails: false,
		Fallback:   false,
	}).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true, Fallback: &fallbackEnabled},
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
	if compiled.Policy.Features.Fallback {
		t.Fatal("Policy.Features.Fallback = true, want false")
	}
	if compiled.Pipeline != nil {
		t.Fatal("compiled pipeline is not nil")
	}
	if compiled.Policy.GuardrailsHash != "" {
		t.Fatalf("compiled guardrails hash = %q, want empty", compiled.Policy.GuardrailsHash)
	}
}

func TestCompilerCompile_DefaultsFallbackEnabledWhenUnset(t *testing.T) {
	compiled, err := NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features: FeatureFlags{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if compiled == nil || compiled.Policy == nil {
		t.Fatal("Compile() returned nil policy")
	}
	if !compiled.Policy.Features.Fallback {
		t.Fatal("Policy.Features.Fallback = false, want true")
	}
}

func TestCompilerCompile_ReturnsGatewayErrorWhenGuardrailsCatalogIsEmpty(t *testing.T) {
	_, err := NewCompiler(guardrails.NewRegistry()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
			Guardrails: []GuardrailStep{
				{Ref: "policy-system", Step: 10},
			},
		},
	})
	if err == nil {
		t.Fatal("Compile() error = nil, want gateway error")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("Compile() error = %T, want *core.GatewayError", err)
	}
}

func TestCompilerCompile_WrapsBuildPipelineErrorsAsGatewayErrors(t *testing.T) {
	registry := guardrails.NewRegistry()
	rule, err := guardrails.NewSystemPromptGuardrail("present", guardrails.SystemPromptInject, "be precise")
	if err != nil {
		t.Fatalf("NewSystemPromptGuardrail() error = %v", err)
	}
	if err := registry.Register(rule, responsecache.GuardrailRuleDescriptor{
		Name:    "present",
		Type:    "system_prompt",
		Mode:    string(guardrails.SystemPromptInject),
		Content: "be precise",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	_, err = NewCompiler(registry).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
			Guardrails: []GuardrailStep{
				{Ref: "missing", Step: 10},
			},
		},
	})
	if err == nil {
		t.Fatal("Compile() error = nil, want gateway error")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("Compile() error = %T, want *core.GatewayError", err)
	}
}
