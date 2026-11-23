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

// h3ImageModels are the models MiniMax serves from the OpenAI-shaped
// /images/generations endpoint rather than the legacy /image_generation one.
var h3ImageModels = []string{"MiniMax-H3-Image", "MiniMax-H3-Image-Highspeed"}

// h3ImageModel returns MiniMax's spelling of an H3 image model ID. The match
// ignores case and surrounding space, and the canonical ID is what goes
// upstream, so a loosely spelled model is not routed to H3 and then rejected.
func h3ImageModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	for _, id := range h3ImageModels {
		if strings.EqualFold(model, id) {
			return id, true
		}
	}
	return "", false
}

// createH3Image sends a non-replayable POST. H3 may finish and charge for
// the image even when the client never receives a response.
func (p *Provider) createH3Image(ctx context.Context, req *core.ImageGenerationRequest, model string) (*core.ImageGenerationResponse, error) {
	if req.ImageCount() > 10 {
		return nil, core.NewInvalidRequestError("minimax H3 supports up to 10 images per request", nil)
	}
	cloned := *req
	cloned.Model = model
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
		Method: http.MethodPost, Endpoint: "/images/generations", Model: model,
		RawBodyReader: io.NopCloser(bytes.NewReader(body)),
		Headers:       http.Header{"Content-Type": {"application/json"}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	// A partially successful batch is returned as it is: its images are
	// already billed, so one failed or empty item must not discard them.
	if resp.GeneratedImageCount() == 0 {
		return nil, h3NoImagesError(&resp)
	}
	return &resp, nil
}

// h3NoImagesError reports an HTTP 200 answer without a single image. MiniMax
// documents 402, 422, or 500 for a batch that failed entirely, so this is an
// unexpected answer; the first item error and the request ID are kept so the
// call can still be diagnosed and reported to MiniMax.
func h3NoImagesError(resp *core.ImageGenerationResponse) error {
	var message strings.Builder
	message.WriteString("minimax H3 response contains no images")
	for _, item := range resp.Data {
		if item.Error == nil {
			continue
		}
		if detail := strings.TrimSpace(item.Error.Message); detail != "" {
			message.WriteString(": " + detail)
		}
		if code := strings.TrimSpace(item.Error.Code); code != "" {
			message.WriteString(" (" + code + ")")
		}
		break
	}
	if requestID := strings.TrimSpace(resp.RequestID); requestID != "" {
		message.WriteString(", request_id " + requestID)
	}
	return core.NewProviderError("minimax", http.StatusBadGateway, truncateStatusMessage(message.String()), nil)
}
