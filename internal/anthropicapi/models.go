package anthropicapi

import (
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// ModelsList is the Anthropic /v1/models response body.
type ModelsList struct {
	Data    []ModelInfo `json:"data"`
	HasMore bool        `json:"has_more"`
	FirstID *string     `json:"first_id"`
	LastID  *string     `json:"last_id"`
}

// ModelInfo is one model entry in the Anthropic models list.
type ModelInfo struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// FromModels renders the catalog in the Anthropic models-list shape. The full
// catalog is returned in one page: has_more is always false, so SDK
// auto-pagination terminates after a single request.
func FromModels(models []core.Model) *ModelsList {
	out := &ModelsList{Data: make([]ModelInfo, 0, len(models))}
	for _, model := range models {
		out.Data = append(out.Data, ModelInfo{
			Type:        "model",
			ID:          model.ID,
			DisplayName: modelDisplayName(model),
			CreatedAt:   time.Unix(model.Created, 0).UTC().Format(time.RFC3339),
		})
	}
	if len(out.Data) > 0 {
		out.FirstID = &out.Data[0].ID
		out.LastID = &out.Data[len(out.Data)-1].ID
	}
	return out
}

func modelDisplayName(model core.Model) string {
	if model.Metadata != nil && model.Metadata.DisplayName != "" {
		return model.Metadata.DisplayName
	}
	return model.ID
}
