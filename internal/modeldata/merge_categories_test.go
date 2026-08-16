package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// Categories are derived data: an operator declaring modes in config must get
// the matching categories without knowing about the internal categories field.
func TestMergeMetadata_DerivesCategoriesFromOverrideModes(t *testing.T) {
	t.Run("nil base", func(t *testing.T) {
		merged := MergeMetadata(nil, &core.ModelMetadata{Modes: []string{"embedding"}})
		if len(merged.Categories) != 1 || merged.Categories[0] != core.CategoryEmbedding {
			t.Errorf("Categories = %v, want [embedding]", merged.Categories)
		}
	})

	t.Run("replaces stale base categories", func(t *testing.T) {
		base := &core.ModelMetadata{
			Modes:      []string{"chat"},
			Categories: []core.ModelCategory{core.CategoryTextGeneration},
		}
		merged := MergeMetadata(base, &core.ModelMetadata{Modes: []string{"embedding"}})
		if len(merged.Modes) != 1 || merged.Modes[0] != "embedding" {
			t.Errorf("Modes = %v, want [embedding]", merged.Modes)
		}
		if len(merged.Categories) != 1 || merged.Categories[0] != core.CategoryEmbedding {
			t.Errorf("Categories = %v, want [embedding]", merged.Categories)
		}
	})

	t.Run("explicit override categories win", func(t *testing.T) {
		merged := MergeMetadata(nil, &core.ModelMetadata{
			Modes:      []string{"embedding"},
			Categories: []core.ModelCategory{core.CategoryUtility},
		})
		if len(merged.Categories) != 1 || merged.Categories[0] != core.CategoryUtility {
			t.Errorf("Categories = %v, want [utility]", merged.Categories)
		}
	})

	t.Run("no modes leaves base categories alone", func(t *testing.T) {
		base := &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryTextGeneration}}
		merged := MergeMetadata(base, &core.ModelMetadata{DisplayName: "X"})
		if len(merged.Categories) != 1 || merged.Categories[0] != core.CategoryTextGeneration {
			t.Errorf("Categories = %v, want [text_generation]", merged.Categories)
		}
	})
}
