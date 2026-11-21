package minimax

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/goccy/go-json"
)

// createH3Image sends a non-replayable POST. H3 may finish and charge for
// the image even when the client never receives a response.
func (p *Provider) createH3Image(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if req.ImageCount() > 10 {
		return nil, core.NewInvalidRequestError("minimax H3 supports up to 10 images per request", nil)
	}
	cloned := *req
	cloned.User, cloned.Provider = "", ""
	switch cloned.ResponseFormat {
	case "", "url", "b64_json":
	case "base64":
		cloned.ResponseFormat = "b64_json"
	default:
		return nil, core.NewInvalidRequestError("minimax H3 supports url or b64_json response formats", nil)
	}

	body, err := json.Marshal(cloned)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax H3 image request", err)
	}
	var resp core.ImageGenerationResponse
	err = p.Do(ctx, llmclient.Request{
		Method: http.MethodPost, Endpoint: "/images/generations", Model: req.Model,
		RawBodyReader: io.NopCloser(bytes.NewReader(body)),
		Headers:       http.Header{"Content-Type": {"application/json"}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	for _, item := range resp.Data {
		if item.Error == nil && strings.TrimSpace(item.URL) == "" && strings.TrimSpace(item.B64JSON) == "" {
			return nil, core.NewProviderError("minimax", http.StatusBadGateway, "H3 image result contains no image or error", nil)
		}
	}
	if resp.GeneratedImageCount() == 0 {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "H3 response contains no successful images", nil)
	}
	return &resp, nil
}
