package server

import (
	"context"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

type usageCaptureLogger struct {
	config  usage.Config
	entries []*usage.UsageEntry
}

func (l *usageCaptureLogger) Write(entry *usage.UsageEntry) {
	l.entries = append(l.entries, entry)
}

func (l *usageCaptureLogger) Config() usage.Config { return l.config }
func (l *usageCaptureLogger) Close() error         { return nil }

func TestTranslatedInferenceService_LogUsageSkipsWhenWorkflowDisablesUsage(t *testing.T) {
	logger := &usageCaptureLogger{
		config: usage.Config{Enabled: true},
	}
	service := &translatedInferenceService{
		usageLogger: logger,
	}

	service.logUsage(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-usage-off",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      false,
				Guardrails: true,
			},
		},
	}, "gpt-5-nano", "openai", "primary-openai", func(*core.ModelPricing) *usage.UsageEntry {
		return &usage.UsageEntry{ID: "usage-1"}
	})

	if len(logger.entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(logger.entries))
	}
}

func TestTranslatedInferenceService_ProviderTypeForSelectorPrefersExplicitProvider(t *testing.T) {
	service := &translatedInferenceService{
		provider: &mockProvider{},
	}

	got := service.providerTypeForSelector(core.ModelSelector{Provider: "azure", Model: "gpt-4o"}, "openai")
	if got != "azure" {
		t.Fatalf("providerTypeForSelector() = %q, want %q", got, "azure")
	}
}

func TestTranslatedInferenceService_ProviderTypeForSelectorCanonicalizesProviderNameSelectors(t *testing.T) {
	service := &translatedInferenceService{
		provider: &mockProvider{
			supportedModels: []string{"gpt-4o"},
			providerTypes: map[string]string{
				"openai_test/gpt-4o": "openai",
			},
			providerNames: map[string]string{
				"openai_test/gpt-4o": "openai_test",
			},
		},
	}

	got := service.providerTypeForSelector(core.ModelSelector{Provider: "openai_test", Model: "gpt-4o"}, "anthropic")
	if got != "openai" {
		t.Fatalf("providerTypeForSelector() = %q, want %q", got, "openai")
	}
}

func TestQualifyModelWithProvider_PrefixesSlashModelIDs(t *testing.T) {
	got := qualifyModelWithProvider("openai/gpt-4o-mini", "openrouter")
	if got != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("qualifyModelWithProvider() = %q, want %q", got, "openrouter/openai/gpt-4o-mini")
	}
}

func TestQualifyModelWithProvider_KeepsAlreadyQualifiedModelIDs(t *testing.T) {
	got := qualifyModelWithProvider("openrouter/openai/gpt-4o-mini", "openrouter")
	if got != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("qualifyModelWithProvider() = %q, want unchanged model", got)
	}
}

func TestTranslatedInferenceService_LogUsageAssignsUserPathFromContext(t *testing.T) {
	logger := &usageCaptureLogger{
		config: usage.Config{Enabled: true},
	}
	service := &translatedInferenceService{
		usageLogger: logger,
	}

	ctx := core.WithRequestSnapshot(context.Background(), &core.RequestSnapshot{
		UserPath: "/team/alpha",
	})

	service.logUsage(ctx, nil, "gpt-5-nano", "openai", "primary-openai", func(*core.ModelPricing) *usage.UsageEntry {
		return &usage.UsageEntry{ID: "usage-1"}
	})

	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	if got := logger.entries[0].UserPath; got != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", got)
	}
	if got := logger.entries[0].ProviderName; got != "primary-openai" {
		t.Fatalf("ProviderName = %q, want primary-openai", got)
	}
}

func TestTranslatedInferenceService_WithCacheRequestContextClearsInheritedGuardrailsHash(t *testing.T) {
	service := &translatedInferenceService{
		guardrailsHash: "service-default",
	}
	ctx := core.WithGuardrailsHash(context.Background(), "caller-hash")
	workflow := &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID:      "workflow-1",
			GuardrailsHash: "",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: false,
				Fallback:   true,
			},
		},
	}

	got := service.withCacheRequestContext(ctx, workflow)
	if hash := core.GetGuardrailsHash(got); hash != "" {
		t.Fatalf("guardrails hash = %q, want cleared hash", hash)
	}
}
