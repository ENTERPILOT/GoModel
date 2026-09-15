package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Categories are derived data: an operator declaring modes in config must get
// the matching categories without knowing about the internal categories field.
func TestMergeMetadata_DerivesCategoriesFromOverrideModes(t *testing.T) {
	t.Run("nil base", func(t *testing.T) {
		merged := MergeMetadata(nil, &core.ModelMetadata{Modes: []string{"embedding"}})
		require.Len(t, merged.Categories, 1)
		assert.Equal(t, core.CategoryEmbedding, merged.Categories[0])
	})

	t.Run("replaces stale base categories", func(t *testing.T) {
		base := &core.ModelMetadata{
			Modes:      []string{"chat"},
			Categories: []core.ModelCategory{core.CategoryTextGeneration},
		}
		merged := MergeMetadata(base, &core.ModelMetadata{Modes: []string{"embedding"}})
		require.Len(t, merged.Modes, 1)
		assert.Equal(t, "embedding", merged.Modes[0])
		require.Len(t, merged.Categories, 1)
		assert.Equal(t, core.CategoryEmbedding, merged.Categories[0])
	})

	t.Run("explicit override categories win", func(t *testing.T) {
		merged := MergeMetadata(nil, &core.ModelMetadata{
			Modes:      []string{"embedding"},
			Categories: []core.ModelCategory{core.CategoryUtility},
		})
		require.Len(t, merged.Categories, 1)
		assert.Equal(t, core.CategoryUtility, merged.Categories[0])
	})

	t.Run("no modes leaves base categories alone", func(t *testing.T) {
		base := &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryTextGeneration}}
		merged := MergeMetadata(base, &core.ModelMetadata{DisplayName: "X"})
		require.Len(t, merged.Categories, 1)
		assert.Equal(t, core.CategoryTextGeneration, merged.Categories[0])
	})
}
