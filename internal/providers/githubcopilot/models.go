package githubcopilot

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelsResponse mirrors the parts of Copilot's GET /models the gateway
// uses. Each entry describes far more (billing, picker hints, policy terms);
// only the routing, policy, and limit fields are read.
type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Vendor             string   `json:"vendor"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Policy             struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities struct {
		Type   string `json:"type"`
		Family string `json:"family"`
		Limits struct {
			MaxContextWindowTokens int `json:"max_context_window_tokens"`
			MaxOutputTokens        int `json:"max_output_tokens"`
		} `json:"limits"`
		Supports struct {
			ToolCalls       bool     `json:"tool_calls"`
			Vision          bool     `json:"vision"`
			Streaming       bool     `json:"streaming"`
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
}

// ListModels returns the models the account may call. A model whose policy
// is "disabled" is listed by Copilot but rejected on use until the account
// (or its organization) enables it in the Copilot settings, so it is left
// out. The listing also refreshes the per-model endpoint routes.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	raw, err := p.listModels(ctx)
	if err != nil {
		return nil, core.MarkModelListingUnsupported(err)
	}
	resp := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(raw.Data))}
	for _, entry := range raw.Data {
		if entry.ID == "" || strings.EqualFold(entry.Policy.State, "disabled") {
			continue
		}
		resp.Data = append(resp.Data, entry.toCore())
	}
	return resp, nil
}

// fetchModels refreshes the endpoint routes from /models.
func (p *Provider) fetchModels(ctx context.Context) error {
	_, err := p.listModels(ctx)
	return err
}

func (p *Provider) listModels(ctx context.Context) (*modelsResponse, error) {
	var raw modelsResponse
	if err := p.Do(ctx, llmclient.Request{Method: http.MethodGet, Endpoint: "/models"}, &raw); err != nil {
		return nil, err
	}
	routes := make(map[string][]string, len(raw.Data))
	for _, entry := range raw.Data {
		if entry.ID != "" {
			routes[entry.ID] = entry.SupportedEndpoints
		}
	}
	p.routes.set(routes)
	return &raw, nil
}

func (e modelEntry) toCore() core.Model {
	meta := &core.ModelMetadata{
		DisplayName: e.Name,
		Family:      e.Capabilities.Family,
		Categories:  []core.ModelCategory{core.CategoryTextGeneration},
	}
	if e.Capabilities.Type == "embeddings" {
		meta.Categories = []core.ModelCategory{core.CategoryEmbedding}
	} else {
		meta.Capabilities = map[string]bool{
			"tools":     e.Capabilities.Supports.ToolCalls,
			"vision":    e.Capabilities.Supports.Vision,
			"streaming": e.Capabilities.Supports.Streaming,
			"reasoning": len(e.Capabilities.Supports.ReasoningEffort) > 0,
		}
	}
	if limit := e.Capabilities.Limits.MaxContextWindowTokens; limit > 0 {
		meta.ContextWindow = &limit
	}
	if limit := e.Capabilities.Limits.MaxOutputTokens; limit > 0 {
		meta.MaxOutputTokens = &limit
	}
	return core.Model{ID: e.ID, Object: "model", OwnedBy: e.Vendor, Metadata: meta}
}
