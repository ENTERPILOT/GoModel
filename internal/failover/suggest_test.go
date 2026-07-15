package failover

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestGenerateSuggestions_NilRegistryReturnsEmpty(t *testing.T) {
	got := GenerateSuggestions(nil, &fakeRuleProvider{}, "")
	if len(got) != 0 {
		t.Fatalf("GenerateSuggestions(nil registry) = %d suggestions, want 0", len(got))
	}
}

func TestGenerateSuggestions_SuggestsRankedAlternatives(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	got := GenerateSuggestions(registry, &fakeRuleProvider{}, "")
	if len(got) == 0 {
		t.Fatal("GenerateSuggestions() = no suggestions, want ranked alternatives")
	}
	for _, view := range got {
		if !view.Enabled {
			t.Fatalf("suggestion %q Enabled = false, want true", view.Source)
		}
		if view.ManagedSource != ManagedSourceDashboard {
			t.Fatalf("suggestion %q ManagedSource = %q, want %q", view.Source, view.ManagedSource, ManagedSourceDashboard)
		}
		if len(view.Targets) == 0 {
			t.Fatalf("suggestion %q has no targets", view.Source)
		}
		for _, target := range view.Targets {
			if target == view.Source {
				t.Fatalf("suggestion %q lists itself as a failover target", view.Source)
			}
		}
	}
}

func TestGenerateSuggestions_FiltersByPrimaryModel(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	for _, filter := range []string{"openai/gpt-4o", "gpt-4o"} {
		got := GenerateSuggestions(registry, &fakeRuleProvider{}, filter)
		for _, view := range got {
			if view.Source != "openai/gpt-4o" {
				t.Fatalf("filter %q produced suggestion for %q, want openai/gpt-4o only", filter, view.Source)
			}
		}
	}
}

func TestGenerateSuggestions_SkipsNonTextGenerationModels(t *testing.T) {
	registry := newFakeRegistry(
		modelInfoWithCategories("whisper-1", "openai", "openai", 1000, "whisper", core.CategoryAudio),
	)

	got := GenerateSuggestions(registry, &fakeRuleProvider{}, "")
	if len(got) != 0 {
		t.Fatalf("GenerateSuggestions() = %d suggestions for non-text model, want 0", len(got))
	}
}
