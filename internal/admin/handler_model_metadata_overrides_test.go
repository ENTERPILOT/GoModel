package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/metadataoverrides"
)

type metadataOverrideTestStore struct {
	items map[string]metadataoverrides.Override
}

func (s *metadataOverrideTestStore) List(context.Context) ([]metadataoverrides.Override, error) {
	out := make([]metadataoverrides.Override, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	return out, nil
}

func (s *metadataOverrideTestStore) Upsert(_ context.Context, override metadataoverrides.Override) error {
	s.items[override.Selector] = override
	return nil
}

func (s *metadataOverrideTestStore) Delete(_ context.Context, selector string) error {
	if _, ok := s.items[selector]; !ok {
		return metadataoverrides.ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *metadataOverrideTestStore) Close() error { return nil }

type metadataOverrideTestRegistry struct {
	applied map[string]map[string]*core.ModelMetadata
}

func (r *metadataOverrideTestRegistry) ProviderNames() []string { return []string{"pollinations"} }

func (r *metadataOverrideTestRegistry) SetDashboardMetadataOverrides(overrides map[string]map[string]*core.ModelMetadata) {
	r.applied = overrides
}

func TestModelMetadataOverrideLifecycle(t *testing.T) {
	registry := &metadataOverrideTestRegistry{}
	service, err := metadataoverrides.NewService(&metadataOverrideTestStore{items: map[string]metadataoverrides.Override{}}, registry)
	require.NoError(t, err)
	h := NewHandler(nil, nil, WithMetadataOverrides(service))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/model-metadata-overrides",
		`{"selector":"pollinations/flux","metadata":{"categories":["image"],"capabilities":{"vision":true},"context_window":4096}}`)
	require.NoError(t, h.UpsertModelMetadataOverride(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	saved := echotest.Decode[metadataoverrides.Override](t, rec)
	assert.Equal(t, "pollinations", saved.ProviderName)
	assert.Equal(t, "flux", saved.Model)
	assert.Equal(t, []core.ModelCategory{core.CategoryImage}, saved.Metadata.Categories)
	require.NotNil(t, registry.applied["pollinations"]["flux"])
	assert.Equal(t, 4096, *registry.applied["pollinations"]["flux"].ContextWindow)

	c, rec = echotest.Get(t, "/admin/model-metadata-overrides")
	require.NoError(t, h.ListModelMetadataOverrides(c))
	listed := echotest.Decode[[]metadataoverrides.Override](t, rec)
	require.Len(t, listed, 1)
	assert.Equal(t, "pollinations/flux", listed[0].Selector)

	c, rec = echotest.Request(t, http.MethodDelete, "/admin/model-metadata-overrides", `{"selector":"pollinations/flux"}`)
	require.NoError(t, h.DeleteModelMetadataOverride(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, registry.applied)

	c, rec = echotest.Request(t, http.MethodDelete, "/admin/model-metadata-overrides", `{"selector":"pollinations/flux"}`)
	require.NoError(t, h.DeleteModelMetadataOverride(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestModelMetadataOverrideErrors(t *testing.T) {
	newHandler := func(t *testing.T) *Handler {
		service, err := metadataoverrides.NewService(&metadataOverrideTestStore{items: map[string]metadataoverrides.Override{}}, &metadataOverrideTestRegistry{})
		require.NoError(t, err)
		return NewHandler(nil, nil, WithMetadataOverrides(service))
	}
	tests := []struct {
		name     string
		handler  func(t *testing.T) *Handler
		body     string
		wantCode int
	}{
		{"feature unavailable", func(*testing.T) *Handler { return NewHandler(nil, nil) }, `{"selector":"pollinations/flux","metadata":{"context_window":1}}`, http.StatusServiceUnavailable},
		{"missing selector", newHandler, `{"metadata":{"context_window":1}}`, http.StatusBadRequest},
		{"unknown provider", newHandler, `{"selector":"other/flux","metadata":{"context_window":1}}`, http.StatusBadRequest},
		{"empty metadata", newHandler, `{"selector":"pollinations/flux","metadata":{}}`, http.StatusBadRequest},
		{"unsupported capability", newHandler, `{"selector":"pollinations/flux","metadata":{"capabilities":{"tools":true}}}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Request(t, http.MethodPut, "/admin/model-metadata-overrides", tt.body)
			require.NoError(t, tt.handler(t).UpsertModelMetadataOverride(c))
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
		})
	}
}
