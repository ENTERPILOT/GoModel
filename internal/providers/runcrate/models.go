package runcrate

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelList decodes Runcrate's /models listing, an OpenAI {"data": [...]} envelope.
type modelList struct {
	Data []modelInfo `json:"data"`
}

type modelInfo struct {
	ID               string        `json:"id"`
	Object           string        `json:"object"`
	Created          int64         `json:"created"`
	OwnedBy          string        `json:"owned_by"`
	Name             string        `json:"name"`
	ContextLength    int           `json:"context_length"`
	OutputModalities []string      `json:"output_modalities"`
	Pricing          *modelPricing `json:"pricing"`
}

// modelPricing holds Runcrate's USD rates per million tokens. The listing also
// carries per-token strings (prompt, completion); the per-million fields are
// the ones read here.
type modelPricing struct {
	Input  *float64 `json:"input"`
	Output *float64 `json:"output"`
}

// modalityModes maps Runcrate's output modalities onto gateway modes. Image,
// video, and audio outputs are absent: the gateway does not route those to
// Runcrate, so models that only produce them are left out of the catalog.
var modalityModes = map[string][]string{
	"text":       {"chat", "responses"},
	"embeddings": {"embedding"},
}

// ListModels returns Runcrate's model catalog with the output modality,
// context length, display name, and token prices its listing carries. Models
// with no output the gateway can serve are skipped; models without an output
// modality are kept.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelList
	if err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, core.MarkModelListingUnsupported(err)
	}

	result := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(upstream.Data))}
	for _, model := range upstream.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		metadata, servable := model.metadata()
		if !servable {
			continue
		}
		object := strings.TrimSpace(model.Object)
		if object == "" {
			object = "model"
		}
		result.Data = append(result.Data, core.Model{
			ID:       id,
			Object:   object,
			OwnedBy:  strings.TrimSpace(model.OwnedBy),
			Created:  model.Created,
			Metadata: metadata,
		})
	}
	return result, nil
}

// metadata normalizes one listing entry. It collects the gateway modes of every
// listed output modality and reports false when modalities are listed but none
// is served by the gateway.
func (m modelInfo) metadata() (*core.ModelMetadata, bool) {
	var modes []string
	for _, modality := range m.OutputModalities {
		for _, mode := range modalityModes[strings.ToLower(strings.TrimSpace(modality))] {
			if !slices.Contains(modes, mode) {
				modes = append(modes, mode)
			}
		}
	}
	if len(m.OutputModalities) > 0 && len(modes) == 0 {
		return nil, false
	}

	metadata := &core.ModelMetadata{
		DisplayName: strings.TrimSpace(m.Name),
		Pricing:     m.Pricing.toCore(),
	}
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(metadata.Modes)
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	return metadata, true
}

// toCore converts Runcrate's per-million-token prices. Rates that are all
// zero carry no usable price, so they yield nil rather than recording the
// model as free.
func (p *modelPricing) toCore() *core.ModelPricing {
	if p == nil || (isZero(p.Input) && isZero(p.Output)) {
		return nil
	}
	return &core.ModelPricing{
		Currency:      "USD",
		InputPerMtok:  cloneFloat(p.Input),
		OutputPerMtok: cloneFloat(p.Output),
	}
}

func isZero(value *float64) bool {
	return value == nil || *value == 0
}

// cloneFloat copies optional prices so the normalized model does not retain upstream pointers.
func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
