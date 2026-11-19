package mistral

import (
	"context"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

var _ core.ModerationProvider = (*Provider)(nil)

// moderationRequest is Mistral's POST /v1/moderations body: text only, one
// result per string.
type moderationRequest struct {
	Model    string          `json:"model"`
	Input    []string        `json:"input"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// moderationResponse is Mistral's moderation reply. It has no flagged field.
type moderationResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

// CreateModeration classifies text through Mistral's moderation endpoint
// (mistral-moderation-latest) and answers in OpenAI's shape. Mistral's own
// category names (violence_and_threats, pii, ...) are kept, flagged is true
// when any category is, and every category applies to text, the only input
// type Mistral accepts. Image inputs are rejected.
func (p *Provider) CreateModeration(ctx context.Context, req *core.ModerationRequest) (*core.ModerationResponse, error) {
	if err := core.ValidateModerationRequest(req); err != nil {
		return nil, err
	}
	inputs, err := req.ModerationInputs()
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if len(input.ImageURLs) > 0 {
			return nil, core.NewInvalidRequestError("mistral moderation accepts text input only; remove the image_url parts", nil)
		}
		texts = append(texts, input.Text)
	}

	var upstream moderationResponse
	err = p.Compatible().Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/moderations",
		Model:     req.Model,
		Operation: llmclient.OperationModerations,
		Body: moderationRequest{
			Model: req.Model,
			Input: texts,
			// metadata is the only other field Mistral's endpoint takes.
			Metadata: req.ExtraFields.Lookup("metadata"),
		},
	}, &upstream)
	if err != nil {
		return nil, err
	}

	resp := &core.ModerationResponse{
		ID:      upstream.ID,
		Model:   upstream.Model,
		Results: make([]core.ModerationResult, 0, len(upstream.Results)),
	}
	for _, r := range upstream.Results {
		result := core.ModerationResult{
			Categories:                r.Categories,
			CategoryScores:            r.CategoryScores,
			CategoryAppliedInputTypes: make(map[string][]string, len(r.Categories)),
		}
		for name, hit := range r.Categories {
			result.Flagged = result.Flagged || hit
			result.CategoryAppliedInputTypes[name] = []string{"text"}
		}
		resp.Results = append(resp.Results, result)
	}
	return resp, nil
}
