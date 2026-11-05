package together

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelList decodes Together's /models listing, which is a bare JSON array,
// and also accepts the OpenAI {"data": [...]} envelope.
type modelList []modelInfo

func (l *modelList) UnmarshalJSON(data []byte) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(trimmed, (*[]modelInfo)(l))
	}
	var envelope struct {
		Data []modelInfo `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	*l = envelope.Data
	return nil
}

type modelInfo struct {
	ID            string        `json:"id"`
	Object        string        `json:"object"`
	Created       int64         `json:"created"`
	Type          string        `json:"type"`
	DisplayName   string        `json:"display_name"`
	Organization  string        `json:"organization"`
	ContextLength int           `json:"context_length"`
	Pricing       *modelPricing `json:"pricing"`
}

// modelPricing holds Together's USD rates per million tokens. Image, audio,
// and video models are priced in other units, but those are not listed.
type modelPricing struct {
	Input  *float64 `json:"input"`
	Output *float64 `json:"output"`
}

// typeModes maps Together's model types onto gateway modes. Image, audio,
// transcribe, video, and rerank models are absent: the gateway does not
// route those to Together, so they are left out of the catalog.
var typeModes = map[string][]string{
	"chat":       {"chat", "responses"},
	"language":   {"chat", "responses"},
	"code":       {"chat", "responses"},
	"moderation": {"chat", "responses"},
	"embedding":  {"embedding"},
}

// ListModels returns Together's model catalog with the type, context length,
// display name, and token prices its listing carries. Models of a type the
// gateway cannot serve are skipped; models without a type are kept.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelList
	if err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, core.MarkModelListingUnsupported(err)
	}

	result := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(upstream))}
	for _, model := range upstream {
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
			OwnedBy:  strings.TrimSpace(model.Organization),
			Created:  model.Created,
			Metadata: metadata,
		})
	}
	return result, nil
}

// metadata normalizes one listing entry. It reports false when the model's
// type is known but not served by the gateway.
func (m modelInfo) metadata() (*core.ModelMetadata, bool) {
	modelType := strings.ToLower(strings.TrimSpace(m.Type))
	modes, known := typeModes[modelType]
	if modelType != "" && !known {
		return nil, false
	}

	metadata := &core.ModelMetadata{
		DisplayName: strings.TrimSpace(m.DisplayName),
		Pricing:     m.Pricing.toCore(),
	}
	if len(modes) > 0 {
		metadata.Modes = append([]string(nil), modes...)
		metadata.Categories = core.CategoriesForModes(metadata.Modes)
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	return metadata, true
}

// toCore converts Together's per-million-token prices. Rates that are all
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
