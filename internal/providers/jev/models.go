package jev

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelsResponse mirrors GET /v1/models. Neither server returns the OpenAI
// list shape: TypeSafe names each model or alias with "name" and describes
// it, while a Kev server names its checkpoint with "id" and lists the aliases
// it answers to, so both spellings are read.
type modelsResponse struct {
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	Name        string   `json:"name"`
	ID          string   `json:"id"`
	Description string   `json:"description"`
	ReleaseDate string   `json:"release_date"`
	Aliases     []string `json:"aliases"`
}

// ListModels returns the names a request's model field may carry. Aliases
// are listed as models of their own, so a request that names one (jev-latest
// on a Kev server) resolves against the catalog like any other model.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var raw modelsResponse
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models",
	}, &raw); err != nil {
		return nil, err
	}
	return raw.toCore(), nil
}

func (r *modelsResponse) toCore() *core.ModelsResponse {
	resp := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(r.Models))}
	seen := make(map[string]struct{}, len(r.Models))
	for _, entry := range r.Models {
		for _, id := range entry.ids() {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			resp.Data = append(resp.Data, entry.toCore(id))
		}
	}
	return resp
}

// ids returns the model's own name followed by its aliases, blanks dropped.
func (e modelEntry) ids() []string {
	ids := make([]string, 0, 1+len(e.Aliases))
	name := strings.TrimSpace(e.Name)
	if name == "" {
		name = strings.TrimSpace(e.ID)
	}
	if name != "" {
		ids = append(ids, name)
	}
	for _, alias := range e.Aliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			ids = append(ids, alias)
		}
	}
	return ids
}

// toCore describes one name from the entry. System One models are decision
// models with no generation mode the gateway could route an OpenAI request
// to, so they are categorized as utility models and claim no mode.
func (e modelEntry) toCore(id string) core.Model {
	return core.Model{
		ID:      id,
		Object:  "model",
		Created: releaseTimestamp(e.ReleaseDate),
		Metadata: &core.ModelMetadata{
			Description: strings.TrimSpace(e.Description),
			Categories:  []core.ModelCategory{core.CategoryUtility},
		},
	}
}

// releaseTimestamp converts a release date to the Unix timestamp the OpenAI
// model shape carries, or 0 when the entry has none or it is not a date.
func releaseTimestamp(releaseDate string) int64 {
	releaseDate = strings.TrimSpace(releaseDate)
	if releaseDate == "" {
		return 0
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if parsed, err := time.Parse(layout, releaseDate); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}
