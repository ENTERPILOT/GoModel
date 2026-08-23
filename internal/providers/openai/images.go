package openai

import (
	"context"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// CreateImage implements OpenAI image generation (POST /images/generations).
// The typed request marshals its known fields plus any extra parameters the
// client sent, so model-specific options (style, background, output_format,
// moderation, ...) reach the upstream unchanged.
func (p *CompatibleProvider) CreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if err := core.ValidateImageGenerationRequest(req); err != nil {
		return nil, err
	}
	var resp core.ImageGenerationResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/images/generations",
		Model:    req.Model,
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}
	if resp.Data == nil {
		resp.Data = []core.ImageData{}
	}
	return &resp, nil
}
