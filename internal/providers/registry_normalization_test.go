package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// The registry and router trim caller input at the boundary and store
// normalized names, types, and model IDs. These tests pin the observable
// behavior so trims on the read paths can be removed without changing it.

func TestRegistryTrimsProviderRegistrationInput(t *testing.T) {
	provider := &mockProvider{name: "west"}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "  west  ", " openai ")
	got := registry.ProviderNames()
	require.Len(t, got, 1)
	require.Equal(t, "west", got[0])
	got = registry.ProviderTypes()
	require.Len(t, got, 1)
	require.Equal(t, "openai", got[0])

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
			got := registry.GetProviderTypeForName(tt.input)
			require.Equal(t, tt.wantType, got, "GetProviderTypeForName(%q)", tt.input)

			if tt.wantType == "" {
				require.Nil(t, registry.ProviderByName(tt.input))
			} else {
				require.NotNil(t, registry.ProviderByName(tt.input))
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
			got := registry.GetProviderNameForType(tt.input)
			require.Equal(t, tt.wantName, got, "GetProviderNameForType(%q)", tt.input)

			if tt.wantName == "" {
				require.Nil(t, registry.ProviderByType(tt.input))
			} else {
				require.NotNil(t, registry.ProviderByType(tt.input))
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
			got := registry.Supports(tt.selector)
			require.Equal(t, tt.wantFound, got, "Supports(%q)", tt.selector)
			got = registry.GetProvider(tt.selector) != nil
			require.Equal(t, tt.wantFound, got, "GetProvider(%q) found", tt.selector)

			wantType, wantName := "", ""
			if tt.wantFound {
				wantType, wantName = "openai", "west"
			}
			require.Equal(t, wantType, registry.GetProviderType(tt.selector))
			require.Equal(t, wantName, registry.GetProviderName(tt.selector))
			model, ok := registry.LookupModel(tt.selector)
			require.Equal(t, tt.wantFound, ok)
			if ok {
				require.Equal(t, "gpt-4o", model.ID)
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
			require.Equal(t, tt.wantOK, ok)

			if ok {
				require.Equal(t, "west/gpt-4o", sel.QualifiedModel())
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
	require.NoError(t, err)

	tests := []struct {
		name         string
		model        string
		providerHint string
		wantResolved string
		wantType     string
		wantErr      bool
	}{
		{name: "bare", model: "gpt-4o", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "bare padded", model: "  gpt-4o  ", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "name qualified padded", model: " west/gpt-4o ", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "type qualified padded", model: " openai/gpt-4o ", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "hint padded", model: " gpt-4o ", providerHint: " west ", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "type hint padded", model: "gpt-4o", providerHint: " openai ", wantResolved: "west/gpt-4o", wantType: "openai"},
		{name: "unknown", model: "east/gpt-4o", wantResolved: "east/gpt-4o", wantType: ""},
		{name: "blank", model: "   ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector, _, err := router.ResolveModel(core.NewRequestedModelSelector(tt.model, tt.providerHint))
			require.Equal(t, tt.wantErr, err != nil)

			if err != nil {
				return
			}
			got := selector.QualifiedModel()
			require.Equal(t, tt.wantResolved, got)
			got = router.GetProviderType(tt.model)
			require.Equal(t, tt.wantType, got, "GetProviderType(%q)", tt.model)
		})
	}

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "  openai/gpt-4o  ", Provider: "  "})
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-west", resp.ID)
	require.Equal(t, "openai", resp.Provider, "ChatCompletion() = %+v, want west response stamped openai", resp)
	require.NotNil(t, provider.lastChatReq)
	require.Equal(t, "gpt-4o", provider.lastChatReq.Model)
	require.Equal(t, "west", router.GetProviderNameForType(" openai "))
	require.Equal(t, "openai", router.GetProviderTypeForName(" west "))
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
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	models := registry.ListModels()
	require.Len(t, models, 1)
	require.Equal(t, "gpt-4o", models[0].ID)

	for _, selector := range []string{"gpt-4o", "west/gpt-4o"} {
		require.True(t, registry.Supports(selector), "Supports(%q)", selector)
	}
	sel, ok := registry.ResolveProviderSelector("openai", "gpt-4o")
	require.True(t, ok)
	require.Equal(t, "west/gpt-4o", sel.QualifiedModel())

	router, err := NewRouter(registry)
	require.NoError(t, err)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o"})
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-west", resp.ID)
	require.NotNil(t, provider.lastChatReq)
	require.Equal(t, "gpt-4o", provider.lastChatReq.Model, "ChatCompletion() = %+v, forwarded %+v; want west response for gpt-4o", resp, provider.lastChatReq)
}

// A provider that lists "foo" and " foo " produces one record after trimming.
// The provider-scoped map and the bare-ID map must both keep the first one.
func TestRegistryKeepsFirstDuplicateDiscoveredModel(t *testing.T) {
	provider := &lazyRefreshProvider{
		name: "west",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "foo", Object: "model", OwnedBy: "first", Created: 1},
				{ID: " foo ", Object: "model", OwnedBy: "second", Created: 2},
			},
		},
	}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "west", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	got := registry.ListModels()
	require.Len(t, got, 1)
	require.Equal(t, "foo", got[0].ID)

	qualified := registry.GetModel("west/foo")
	bare := registry.GetModel("foo")
	require.NotNil(t, qualified)
	require.NotNil(t, bare)
	require.Same(t, bare, qualified)
	require.Equal(t, "first", qualified.Model.OwnedBy)
	require.Equal(t, int64(1), qualified.Model.Created, "kept record = %+v, want the first listed (owned_by first, created 1)", qualified.Model)
}

// selectorResolverOnlyLookup implements the O(1) qualified-selector fast path
// without the catalog lister the slow path needs.
type selectorResolverOnlyLookup struct {
	*mockModelLookup
	resolved core.ModelSelector
	calls    int
}

func (l *selectorResolverOnlyLookup) ResolveProviderSelector(segment, modelID string) (core.ModelSelector, bool) {
	l.calls++
	if segment == "openai" && modelID == l.resolved.Model {
		return l.resolved, true
	}
	return core.ModelSelector{}, false
}

func TestRouterUsesSelectorResolverWithoutCatalogLister(t *testing.T) {
	lookup := &selectorResolverOnlyLookup{
		mockModelLookup: newMockLookup(),
		resolved:        core.ModelSelector{Provider: "west", Model: "gpt-4o"},
	}
	lookup.addModel("west/gpt-4o", &mockProvider{name: "west"}, "openai")
	router, err := NewRouter(lookup)
	require.NoError(t, err)
	require.Nil(t, router.caps.modelsWithProvider)

	selector, changed, err := router.ResolveModel(core.NewRequestedModelSelector("openai/gpt-4o", ""))
	require.NoError(t, err)
	got := selector.QualifiedModel()
	require.Equal(t, "west/gpt-4o", got)
	require.True(t, changed)
	require.Equal(t, 1, lookup.calls)

	// A miss on the fast path with no catalog lister leaves the selector as is.
	selector, changed, err = router.ResolveModel(core.NewRequestedModelSelector("openai/other", ""))
	require.NoError(t, err)
	got = selector.QualifiedModel()
	require.Equal(t, "openai/other", got)
	require.False(t, changed)
}

func TestRecordAvailabilityCheckKeepsFailureMarker(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "plain", err: errors.New("boom"), want: "boom"},
		{name: "padded", err: errors.New("  boom \n"), want: "boom"},
		{name: "whitespace only", err: errors.New("   "), want: "unknown error"},
		{name: "empty", err: errors.New(""), want: "unknown error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewModelRegistry()
			registry.RegisterProviderWithNameAndType(&mockProvider{name: "west"}, "west", "openai")
			registry.RecordAvailabilityCheck("west", tt.err)
			got := registry.providerRuntime["west"].lastAvailabilityError
			require.Equal(t, tt.want, got)
			require.Equal(t, []string{"west"}, registry.FailedProviderNames())
			snapshots := registry.ProviderRuntimeSnapshots()
			require.Len(t, snapshots, 1)
			require.Equal(t, tt.want, snapshots[0].LastAvailabilityError)
		})
	}
}
