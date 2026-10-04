package metadataoverrides

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

type memoryStore struct {
	items map[string]Override
}

func (s *memoryStore) List(context.Context) ([]Override, error) {
	out := make([]Override, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	return out, nil
}

func (s *memoryStore) Upsert(_ context.Context, override Override) error {
	s.items[override.Selector] = override
	return nil
}

func (s *memoryStore) Delete(_ context.Context, selector string) error {
	if _, ok := s.items[selector]; !ok {
		return ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *memoryStore) Close() error { return nil }

type fakeRegistry struct {
	providers []string
	applied   map[string]map[string]*core.ModelMetadata
}

func (r *fakeRegistry) ProviderNames() []string { return r.providers }

func (r *fakeRegistry) SetDashboardMetadataOverrides(overrides map[string]map[string]*core.ModelMetadata) {
	r.applied = overrides
}

func newTestService(t *testing.T, rows ...Override) (*Service, *memoryStore, *fakeRegistry) {
	t.Helper()
	store := &memoryStore{items: map[string]Override{}}
	for _, row := range rows {
		store.items[row.Selector] = row
	}
	registry := &fakeRegistry{providers: []string{"pollinations", "openrouter"}}
	service, err := NewService(store, registry)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))
	return service, store, registry
}

func TestServiceUpsertNormalizesAndApplies(t *testing.T) {
	service, store, registry := newTestService(t)

	saved, err := service.Upsert(context.Background(), Override{
		Selector: " openrouter/meta-llama/llama-3.2-vision ",
		Metadata: Metadata{
			Categories:   []core.ModelCategory{core.CategoryImage, core.CategoryTextGeneration, core.CategoryImage},
			Capabilities: map[string]bool{"vision": true},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "openrouter/meta-llama/llama-3.2-vision", saved.Selector)
	assert.Equal(t, "openrouter", saved.ProviderName)
	assert.Equal(t, "meta-llama/llama-3.2-vision", saved.Model)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration, core.CategoryImage}, saved.Metadata.Categories)
	assert.Contains(t, store.items, saved.Selector)

	applied := registry.applied["openrouter"]["meta-llama/llama-3.2-vision"]
	require.NotNil(t, applied)
	assert.True(t, applied.Capabilities["vision"])
	assert.Equal(t, saved.Metadata.Categories, applied.Categories)

	got, ok := service.Get("openrouter/meta-llama/llama-3.2-vision")
	require.True(t, ok)
	assert.Equal(t, saved, got)
}

func TestServiceUpsertRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		override Override
		wantErr  string
	}{
		{"model-wide selector", Override{Selector: "flux", Metadata: Metadata{ContextWindow: new(1)}}, "provider/model"},
		{"provider-wide selector", Override{Selector: "pollinations/", Metadata: Metadata{ContextWindow: new(1)}}, "provider/model"},
		{"global selector", Override{Selector: "/", Metadata: Metadata{ContextWindow: new(1)}}, "provider/model"},
		{"empty metadata", Override{Selector: "pollinations/flux"}, "sets no fields"},
		{"unknown category", Override{Selector: "pollinations/flux", Metadata: Metadata{Categories: []core.ModelCategory{"music"}}}, "unknown category"},
		{"all category", Override{Selector: "pollinations/flux", Metadata: Metadata{Categories: []core.ModelCategory{core.CategoryAll}}}, "unknown category"},
		{"non-input capability", Override{Selector: "pollinations/flux", Metadata: Metadata{Capabilities: map[string]bool{"function_calling": true}}}, "unsupported capability"},
		{"zero context window", Override{Selector: "pollinations/flux", Metadata: Metadata{ContextWindow: new(0)}}, "context_window"},
		{"negative max output", Override{Selector: "pollinations/flux", Metadata: Metadata{MaxOutputTokens: new(-5)}}, "max_output_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, store, _ := newTestService(t)
			_, err := service.Upsert(context.Background(), tt.override)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, store.items)
		})
	}
}

func TestServiceRefreshSkipsInvalidStoredRows(t *testing.T) {
	service, _, registry := newTestService(t,
		Override{Selector: "pollinations/flux", ProviderName: "pollinations", Model: "flux", Metadata: Metadata{ContextWindow: new(1000)}},
		Override{Selector: "pollinations/broken", ProviderName: "pollinations", Model: "broken"},
	)

	list := service.List()
	require.Len(t, list, 1)
	assert.Equal(t, "pollinations/flux", list[0].Selector)
	assert.Len(t, registry.applied["pollinations"], 1)
}

func TestServiceDeleteRevertsRegistryLayer(t *testing.T) {
	service, _, registry := newTestService(t,
		Override{Selector: "pollinations/flux", ProviderName: "pollinations", Model: "flux", Metadata: Metadata{ContextWindow: new(1000)}},
	)
	require.NotEmpty(t, registry.applied)

	require.NoError(t, service.Delete(context.Background(), "pollinations/flux"))
	assert.Empty(t, registry.applied)
	assert.Empty(t, service.List())
	assert.ErrorIs(t, service.Delete(context.Background(), "pollinations/flux"), ErrNotFound)
}
