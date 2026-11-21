package geminiapi

import (
	"slices"

	"github.com/enterpilot/gomodel/internal/core"
)

const modelResourcePrefix = "models/"

// FromModels renders the catalog in the Gemini models-list shape. The whole
// catalog is one page: nextPageToken is never set, so SDK pagers stop after
// a single request.
func FromModels(models []core.Model) *ModelsList {
	out := &ModelsList{Models: make([]ModelInfo, 0, len(models))}
	for _, model := range models {
		out.Models = append(out.Models, FromModel(model))
	}
	return out
}

// FromModel renders one model in the Gemini model shape. The gateway's model
// ID, provider prefix included, follows "models/", so the name a client reads
// from the list is the one it sends back in a request path.
func FromModel(model core.Model) ModelInfo {
	info := ModelInfo{
		Name:                       modelResourcePrefix + model.ID,
		DisplayName:                model.ID,
		SupportedGenerationMethods: []string{},
	}
	metadata := model.Metadata
	if metadata == nil || generatesContent(metadata) {
		info.SupportedGenerationMethods = []string{core.GeminiMethodGenerateContent}
	}
	if metadata == nil {
		return info
	}
	if metadata.DisplayName != "" {
		info.DisplayName = metadata.DisplayName
	}
	info.Description = metadata.Description
	if metadata.ContextWindow != nil {
		info.InputTokenLimit = *metadata.ContextWindow
	}
	if metadata.MaxOutputTokens != nil {
		info.OutputTokenLimit = *metadata.MaxOutputTokens
	}
	return info
}

// generatesContent reports whether a model serves chat, which is what
// generateContent is translated to: its registry modes name chat or
// responses. A model with no recorded mode is assumed to, unless it is
// catalogued as a utility model, as the System One decision models are.
func generatesContent(metadata *core.ModelMetadata) bool {
	if len(metadata.Modes) == 0 {
		return !slices.Contains(metadata.Categories, core.CategoryUtility)
	}
	return slices.ContainsFunc(metadata.Modes, func(mode string) bool {
		return mode == "chat" || mode == "responses"
	})
}
