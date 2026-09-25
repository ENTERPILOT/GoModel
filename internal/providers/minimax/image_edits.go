package minimax

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/goccy/go-json"
)

var _ core.ImageEditProvider = (*Provider)(nil)

// liveModel reports whether model is MiniMax's image-01-live variant, which
// composes from aspect_ratio only and ignores explicit pixel dimensions.
func liveModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "image-01-live")
}

// CreateImageEdit generates images using uploaded character portrait references.
func (p *Provider) CreateImageEdit(ctx context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	if err := core.ValidateImageEditRequest(req); err != nil {
		return nil, err
	}
	if req.Mask != nil {
		return nil, core.NewInvalidRequestError("minimax image edits do not support masks", nil)
	}
	references := make([]map[string]string, 0, len(req.Images))
	for _, img := range req.Images {
		mime := http.DetectContentType(img.Data)
		if mime != "image/jpeg" && mime != "image/png" {
			return nil, core.NewInvalidRequestError("minimax reference images must be JPEG or PNG", nil)
		}
		if len(img.Data) >= 10*1024*1024 {
			return nil, core.NewInvalidRequestError("minimax reference images must be smaller than 10 MB", nil)
		}
		references = append(references, map[string]string{"type": "character", "image_file": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)})
	}
	payload := map[string]any{"model": req.Model, "prompt": req.Prompt, "subject_reference": references}
	format := "url"
	// MiniMax honours width and height only for image-01; image-01-live ignores
	// them and would return a differently sized image, so reject the dimension
	// fields instead of accepting a request that cannot be satisfied.
	live := liveModel(req.Model)
	for _, field := range req.Fields {
		if live {
			switch field.Name {
			case "width", "height", "size":
				return nil, core.NewInvalidRequestError("minimax "+field.Name+" is supported only by image-01; use aspect_ratio with image-01-live", nil)
			}
		}
		switch field.Name {
		case "response_format":
			format = field.Value
			if format == "b64_json" {
				format = "base64"
			}
			if format != "url" && format != "base64" {
				return nil, core.NewInvalidRequestError("minimax response_format must be url, base64, or b64_json", nil)
			}
			payload[field.Name] = format
		case "n", "seed":
			value, err := strconv.ParseInt(field.Value, 10, 64)
			if err != nil {
				return nil, core.NewInvalidRequestError(field.Name+" must be an integer", err)
			}
			payload[field.Name] = value
		case "width", "height":
			value, err := strconv.Atoi(field.Value)
			if err != nil {
				return nil, core.NewInvalidRequestError(field.Name+" must be an integer", err)
			}
			if !validImageDimension(value) {
				return nil, core.NewInvalidRequestError(imageDimensionError, nil)
			}
			payload[field.Name] = value
		case "prompt_optimizer":
			value, err := strconv.ParseBool(field.Value)
			if err != nil {
				return nil, core.NewInvalidRequestError("prompt_optimizer must be a boolean", err)
			}
			payload[field.Name] = value
		case "size":
			parts := strings.Split(field.Value, "x")
			if len(parts) != 2 {
				return nil, core.NewInvalidRequestError("size must be WIDTHxHEIGHT", nil)
			}
			for i, name := range []string{"width", "height"} {
				value, err := strconv.Atoi(parts[i])
				if err != nil {
					return nil, core.NewInvalidRequestError("size must be WIDTHxHEIGHT", err)
				}
				if !validImageDimension(value) {
					return nil, core.NewInvalidRequestError(imageDimensionError, nil)
				}
				payload[name] = value
			}
		case "aspect_ratio":
			payload[field.Name] = field.Value
		case "stream", "user": // Routing validation rejects streaming; user is gateway metadata.
		default:
			return nil, core.NewInvalidRequestError("minimax image edits do not support field "+field.Name, nil)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax image request", err)
	}
	upstream, err := p.Passthrough(ctx, &core.PassthroughRequest{Method: http.MethodPost, Endpoint: "/image_generation", Body: io.NopCloser(bytes.NewReader(body)), Headers: http.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return nil, err
	}
	if upstream == nil || upstream.Body == nil {
		return nil, core.NewEmptyProviderResponseError("minimax")
	}
	defer func() { _ = upstream.Body.Close() }()
	responseBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to read image response", err)
	}
	if upstream.StatusCode < http.StatusOK || upstream.StatusCode >= http.StatusMultipleChoices {
		return nil, core.ParseProviderError("minimax", upstream.StatusCode, responseBody, nil)
	}
	var response struct {
		Data struct {
			URLs   []string `json:"image_urls"`
			Base64 []string `json:"image_base64"`
		} `json:"data"`
		BaseResponse *struct {
			StatusCode *int   `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to parse image response", err)
	}
	if response.BaseResponse == nil || response.BaseResponse.StatusCode == nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "image response is missing status_code", nil)
	}
	if code := *response.BaseResponse.StatusCode; code != 0 {
		return nil, statusError("image edit", code, response.BaseResponse.StatusMsg, responseBody)
	}
	result := &core.ImageGenerationResponse{Created: time.Now().Unix(), Provider: "minimax", Data: []core.ImageData{}}
	if format == "base64" {
		for _, value := range response.Data.Base64 {
			if value != "" {
				result.Data = append(result.Data, core.ImageData{B64JSON: value})
			}
		}
	} else {
		for _, value := range response.Data.URLs {
			if value != "" {
				result.Data = append(result.Data, core.ImageData{URL: value})
			}
		}
	}
	if len(result.Data) == 0 {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "image response contains no images", nil)
	}
	return result, nil
}
