package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// The registry and router trim caller input at the boundary and store
// normalized names, types, and model IDs. These tests pin the observable
// behavior so trims on the read paths can be removed without changing it.

func TestRegistryTrimsProviderRegistrationInput(t *testing.T) {
	provider := &mockProvider{name: "west"}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "  west  ", " openai ")

	if got := registry.ProviderNames(); len(got) != 1 || got[0] != "west" {
		t.Fatalf("ProviderNames() = %v, want [west]", got)
	}
	if got := registry.ProviderTypes(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("ProviderTypes() = %v, want [openai]", got)
	}

	tests := []struct {
		name     string
		input    string
		wantType string
		wantName string
	}{
		{name: "exact", input: "west", wantType: "openai"},
		{name: "padded", input: "  west  ", wantType: "openai"},
		{name: "unknown", input: "east", wantType: ""},
		{name: "blank", input: "   ", wantType: ""},
	}
	for _, tt := range tests {
		t.Run("name/"+tt.name, func(t *testing.T) {
			if got := registry.GetProviderTypeForName(tt.input); got != tt.wantType {
				t.Fatalf("GetProviderTypeForName(%q) = %q, want %q", tt.input, got, tt.wantType)
			}
			wantProvider := tt.wantType != ""
			if got := registry.ProviderByName(tt.input) != nil; got != wantProvider {
				t.Fatalf("ProviderByName(%q) found = %v, want %v", tt.input, got, wantProvider)
			}
		})
	}

	typeTests := []struct {
		name     string
		input    string
		wantName string
	}{
		{name: "exact", input: "openai", wantName: "west"},
		{name: "padded", input: " openai ", wantName: "west"},
		{name: "unknown", input: "anthropic", wantName: ""},
		{name: "blank", input: "", wantName: ""},
	}
	for _, tt := range typeTests {
		t.Run("type/"+tt.name, func(t *testing.T) {
			if got := registry.GetProviderNameForType(tt.input); got != tt.wantName {
				t.Fatalf("GetProviderNameForType(%q) = %q, want %q", tt.input, got, tt.wantName)
			}
			wantProvider := tt.wantName != ""
			if got := registry.ProviderByType(tt.input) != nil; got != wantProvider {
				t.Fatalf("ProviderByType(%q) found = %v, want %v", tt.input, got, wantProvider)
			}
		})
	}
}

func TestRegistryLookupsTrimSelectorInput(t *testing.T) {
	provider := &mockProvider{name: "west"}
	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     provider,
		providerName: "west",
		providerType: "openai",
		modelID:      "gpt-4o",
	})

	tests := []struct {
		name      string
		selector  string
		wantFound bool
	}{
		{name: "qualified", selector: "west/gpt-4o", wantFound: true},
		{name: "qualified padded", selector: "  west/gpt-4o  ", wantFound: true},
		{name: "qualified inner padded", selector: "west / gpt-4o", wantFound: true},
		{name: "bare", selector: "gpt-4o", wantFound: true},
		{name: "bare padded", selector: " gpt-4o ", wantFound: true},
		{name: "unknown model", selector: "west/nope", wantFound: false},
		{name: "unknown provider", selector: "east/gpt-4o", wantFound: false},
		{name: "blank", selector: "   ", wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := registry.Supports(tt.selector); got != tt.wantFound {
				t.Fatalf("Supports(%q) = %v, want %v", tt.selector, got, tt.wantFound)
			}
			if got := registry.GetProvider(tt.selector) != nil; got != tt.wantFound {
				t.Fatalf("GetProvider(%q) found = %v, want %v", tt.selector, got, tt.wantFound)
			}
			wantType, wantName := "", ""
			if tt.wantFound {
				wantType, wantName = "openai", "west"
			}
			if got := registry.GetProviderType(tt.selector); got != wantType {
				t.Fatalf("GetProviderType(%q) = %q, want %q", tt.selector, got, wantType)
			}
			if got := registry.GetProviderName(tt.selector); got != wantName {
				t.Fatalf("GetProviderName(%q) = %q, want %q", tt.selector, got, wantName)
			}
			model, ok := registry.LookupModel(tt.selector)
			if ok != tt.wantFound {
				t.Fatalf("LookupModel(%q) ok = %v, want %v", tt.selector, ok, tt.wantFound)
			}
			if ok && model.ID != "gpt-4o" {
				t.Fatalf("LookupModel(%q).ID = %q, want gpt-4o", tt.selector, model.ID)
			}
		})
	}
}

func TestRegistryResolveProviderSelectorTrimsInput(t *testing.T) {
	provider := &mockProvider{name: "west"}
	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     provider,
		providerName: "west",
		providerType: "openai",
		modelID:      "gpt-4o",
	})

	tests := []struct {
		name    string
		segment string
		modelID string
		wantOK  bool
	}{
		{name: "by name", segment: "west", modelID: "gpt-4o", wantOK: true},
		{name: "by type", segment: "openai", modelID: "gpt-4o", wantOK: true},
		{name: "padded", segment: " west ", modelID: " gpt-4o ", wantOK: true},
		{name: "unknown", segment: "east", modelID: "gpt-4o", wantOK: false},
		{name: "blank segment", segment: "  ", modelID: "gpt-4o", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, ok := registry.ResolveProviderSelector(tt.segment, tt.modelID)
			if ok != tt.wantOK {
				t.Fatalf("ResolveProviderSelector(%q, %q) ok = %v, want %v", tt.segment, tt.modelID, ok, tt.wantOK)
			}
			if ok && sel.QualifiedModel() != "west/gpt-4o" {
				t.Fatalf("ResolveProviderSelector(%q, %q) = %q, want west/gpt-4o", tt.segment, tt.modelID, sel.QualifiedModel())
			}
		})
	}
}

func TestRouterTrimsSelectorInput(t *testing.T) {
	provider := &mockProvider{
		name:         "west",
		chatResponse: &core.ChatResponse{ID: "chatcmpl-west", Model: "gpt-4o"},
	}
	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     provider,
		providerName: "west",
		providerType: "openai",
		modelID:      "gpt-4o",
	})
	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	tests := []struct {
		name         string
		model        string
		providerHint string
		wantResolved string
		wantErr      bool
	}{
		{name: "bare", model: "gpt-4o", wantResolved: "west/gpt-4o"},
		{name: "bare padded", model: "  gpt-4o  ", wantResolved: "west/gpt-4o"},
		{name: "name qualified padded", model: " west/gpt-4o ", wantResolved: "west/gpt-4o"},
		{name: "type qualified padded", model: " openai/gpt-4o ", wantResolved: "west/gpt-4o"},
		{name: "hint padded", model: " gpt-4o ", providerHint: " west ", wantResolved: "west/gpt-4o"},
		{name: "type hint padded", model: "gpt-4o", providerHint: " openai ", wantResolved: "west/gpt-4o"},
		{name: "unknown", model: "east/gpt-4o", wantResolved: "east/gpt-4o"},
		{name: "blank", model: "   ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector, _, err := router.ResolveModel(core.NewRequestedModelSelector(tt.model, tt.providerHint))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveModel() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got := selector.QualifiedModel(); got != tt.wantResolved {
				t.Fatalf("ResolveModel() = %q, want %q", got, tt.wantResolved)
			}
			if got := router.GetProviderType(tt.model); (got == "openai") != (tt.wantResolved == "west/gpt-4o") {
				t.Fatalf("GetProviderType(%q) = %q", tt.model, got)
			}
		})
	}

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "  openai/gpt-4o  ", Provider: "  "})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.ID != "chatcmpl-west" || resp.Provider != "openai" {
		t.Fatalf("ChatCompletion() = %+v, want west response stamped openai", resp)
	}
	if provider.lastChatReq == nil || provider.lastChatReq.Model != "gpt-4o" {
		t.Fatalf("forwarded model = %+v, want gpt-4o", provider.lastChatReq)
	}
	if router.GetProviderNameForType(" openai ") != "west" || router.GetProviderTypeForName(" west ") != "openai" {
		t.Fatalf("router provider name/type lookups did not trim input")
	}
}

// Discovered model IDs are trimmed when they enter the registry, so a
// provider that pads its IDs stays routable by the clean ID and advertises the
// clean ID in listings.
func TestRegistryNormalizesDiscoveredModelIDs(t *testing.T) {
	provider := &lazyRefreshProvider{
		name:         "west",
		chatResponse: &core.ChatResponse{ID: "chatcmpl-west", Model: "gpt-4o"},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "  gpt-4o  ", Object: "model", OwnedBy: "openai"}},
		},
	}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "west", "openai")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	models := registry.ListModels()
	if len(models) != 1 || models[0].ID != "gpt-4o" {
		t.Fatalf("ListModels() = %+v, want one model with ID gpt-4o", models)
	}
	for _, selector := range []string{"gpt-4o", "west/gpt-4o"} {
		if !registry.Supports(selector) {
			t.Fatalf("Supports(%q) = false, want true", selector)
		}
	}
	if sel, ok := registry.ResolveProviderSelector("openai", "gpt-4o"); !ok || sel.QualifiedModel() != "west/gpt-4o" {
		t.Fatalf("ResolveProviderSelector(openai, gpt-4o) = %q, %v; want west/gpt-4o, true", sel.QualifiedModel(), ok)
	}

	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.ID != "chatcmpl-west" || provider.lastChatReq == nil || provider.lastChatReq.Model != "gpt-4o" {
		t.Fatalf("ChatCompletion() = %+v, forwarded %+v; want west response for gpt-4o", resp, provider.lastChatReq)
	}
}
