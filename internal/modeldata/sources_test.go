package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataSources_NamesTheWinningLayerPerField(t *testing.T) {
	catalog := &core.ModelMetadata{
		DisplayName:     "GPT-4o",
		Description:     "catalog description",
		Modes:           []string{"chat"},
		Categories:      []core.ModelCategory{core.CategoryTextGeneration},
		Tags:            []string{"vision"},
		ContextWindow:   new(128000),
		MaxOutputTokens: new(16384),
		Capabilities:    map[string]bool{"vision": true, "tools": true},
		Rankings:        map[string]core.ModelRanking{"lmarena": {Rank: new(7)}},
	}
	provider := &core.ModelMetadata{
		ContextWindow: new(4096),
		Capabilities:  map[string]bool{"tools": false},
	}
	config := &core.ModelMetadata{
		DisplayName:  "Our GPT-4o",
		Capabilities: map[string]bool{"json_mode": true},
	}
	effective := MergeMetadata(MergeMetadata(catalog, provider), config)

	sources := MetadataSources(effective, provider, catalog, config)

	assert.Equal(t, map[string]string{
		"display_name":           MetadataSourceConfig,
		"description":            MetadataSourceCatalog,
		"modes":                  MetadataSourceCatalog,
		"categories":             MetadataSourceCatalog,
		"tags":                   MetadataSourceCatalog,
		"context_window":         MetadataSourceProvider,
		"max_output_tokens":      MetadataSourceCatalog,
		"capabilities.vision":    MetadataSourceCatalog,
		"capabilities.tools":     MetadataSourceProvider,
		"capabilities.json_mode": MetadataSourceConfig,
		"rankings.lmarena":       MetadataSourceCatalog,
	}, sources)
}

func TestMetadataSources_CategoriesFollowTheLayerThatDeclaredModes(t *testing.T) {
	catalog := &core.ModelMetadata{
		Modes:      []string{"chat"},
		Categories: []core.ModelCategory{core.CategoryTextGeneration},
	}
	config := &core.ModelMetadata{Modes: []string{"embedding"}}
	effective := MergeMetadata(catalog, config)
	require.Equal(t, []core.ModelCategory{core.CategoryEmbedding}, effective.Categories)

	sources := MetadataSources(effective, nil, catalog, config)

	assert.Equal(t, MetadataSourceConfig, sources["modes"])
	assert.Equal(t, MetadataSourceConfig, sources["categories"])
}

func TestMetadataSources_ReportsInferredModes(t *testing.T) {
	effective := &core.ModelMetadata{
		Modes:      []string{"embedding"},
		Categories: []core.ModelCategory{core.CategoryEmbedding},
	}

	sources := MetadataSources(effective, nil, nil, nil)

	assert.Equal(t, map[string]string{
		"modes":      MetadataSourceInferred,
		"categories": MetadataSourceInferred,
	}, sources)
}

func TestMetadataSources_NilEffective(t *testing.T) {
	assert.Nil(t, MetadataSources(nil, &core.ModelMetadata{DisplayName: "x"}, nil, nil))
	assert.Empty(t, MetadataSources(&core.ModelMetadata{}, nil, nil, nil))
}
