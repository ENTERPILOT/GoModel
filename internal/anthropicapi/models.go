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
// auto-pagination terminates after a single request. FromModelsPage serves
// clients that ask for a page.
func FromModels(models []core.Model) *ModelsList {
	out := &ModelsList{Data: make([]ModelInfo, 0, len(models))}
	for _, model := range models {
		out.Data = append(out.Data, FromModel(model))
	}
	if len(out.Data) > 0 {
		out.FirstID = &out.Data[0].ID
		out.LastID = &out.Data[len(out.Data)-1].ID
	}
	return out
}

// ModelsPage selects a page of the models list, following the Anthropic list
// parameters: Limit models (1-1000) after AfterID or before BeforeID. The zero
// value selects the whole list.
type ModelsPage struct {
	Limit    int
	AfterID  string
	BeforeID string
}

// maxModelsPageLimit is the largest page the Anthropic models list allows.
const maxModelsPageLimit = 1000

// FromModelsPage renders one page of the catalog in the Anthropic models-list
// shape. has_more reports whether more models follow in the paging direction,
// so SDK auto-pagination walks the catalog with after_id. A cursor naming no
// listed model yields an empty page.
func FromModelsPage(models []core.Model, page ModelsPage) *ModelsList {
	if page == (ModelsPage{}) {
		return FromModels(models)
	}
	limit := page.Limit
	if limit <= 0 {
		limit = len(models)
	}
	limit = min(limit, maxModelsPageLimit)
	start, end := 0, len(models)
	if page.AfterID != "" {
		start = modelIndex(models, page.AfterID) + 1
		if start == 0 {
			return FromModels(nil)
		}
	}
	if page.BeforeID != "" {
		end = modelIndex(models, page.BeforeID)
		if end < 0 {
			return FromModels(nil)
		}
	}
	if start >= end {
		return FromModels(nil)
	}
	var selected []core.Model
	var hasMore bool
	if page.BeforeID != "" && page.AfterID == "" {
		first := max(start, end-limit)
		selected, hasMore = models[first:end], first > start
	} else {
		last := min(end, start+limit)
		selected, hasMore = models[start:last], last < end
	}
	out := FromModels(selected)
	out.HasMore = hasMore
	return out
}

func modelIndex(models []core.Model, id string) int {
	for i, model := range models {
		if model.ID == id {
			return i
		}
	}
	return -1
}

// FromModel renders one model in the Anthropic model shape, as returned by
// Anthropic's retrieve-model endpoint.
func FromModel(model core.Model) ModelInfo {
	return ModelInfo{
		Type:        "model",
		ID:          model.ID,
		DisplayName: modelDisplayName(model),
		CreatedAt:   time.Unix(model.Created, 0).UTC().Format(time.RFC3339),
	}
}

func modelDisplayName(model core.Model) string {
	if model.Metadata != nil && model.Metadata.DisplayName != "" {
		return model.Metadata.DisplayName
	}
	return model.ID
}
