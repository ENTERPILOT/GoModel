package server

import (
	"sort"

	"gomodel/internal/core"
)

// ExposedModelLister surfaces additional public models to include in GET /v1/models.
type ExposedModelLister interface {
	ExposedModels() []core.Model
}

// FilteredExposedModelLister optionally filters exposed models using their concrete targets.
type FilteredExposedModelLister interface {
	ExposedModelsFiltered(allow func(core.ModelSelector) bool) []core.Model
}

func mergeExposedModelsResponse(base *core.ModelsResponse, exposed []core.Model) *core.ModelsResponse {
	if base == nil {
		base = &core.ModelsResponse{Object: "list", Data: []core.Model{}}
	}
	if len(exposed) == 0 {
		return base
	}

	dataByID := make(map[string]core.Model, len(base.Data)+len(exposed))
	for _, model := range base.Data {
		dataByID[model.ID] = model
	}
	for _, model := range exposed {
		dataByID[model.ID] = model
	}

	data := make([]core.Model, 0, len(dataByID))
	for _, model := range dataByID {
		data = append(data, model)
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })

	cloned := *base
	cloned.Data = data
	return &cloned
}
