package providers

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ModelObject returns a listing entry's trimmed object type, defaulting a
// blank one to "model" as the OpenAI shape expects.
func ModelObject(object string) string {
	if object = strings.TrimSpace(object); object != "" {
		return object
	}
	return "model"
}

// ModelList builds an OpenAI-style "list" response from decoded /models
// entries. Entries whose converted model has a blank ID are left out, so
// convert drops an entry by returning the zero core.Model.
func ModelList[T any](entries []T, convert func(T) core.Model) *core.ModelsResponse {
	result := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(entries))}
	for _, entry := range entries {
		model := convert(entry)
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		result.Data = append(result.Data, model)
	}
	return result
}
