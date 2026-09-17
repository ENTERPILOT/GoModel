package minimax

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/goccy/go-json"
)

var _ core.ImageProvider = (*Provider)(nil)

// CreateImage translates image requests to MiniMax's native image endpoint.
func (p *Provider) CreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if err := core.ValidateImageGenerationRequest(req); err != nil {
		return nil, err
	}
	if req.Quality != "" {
		return nil, core.NewInvalidRequestError("minimax images do not support quality", nil)
	}
	format := req.ResponseFormat
	switch format {
	case "", "url":
		format = "url"
	case "b64_json", "base64":
		format = "base64"
	default:
		return nil, core.NewInvalidRequestError("minimax images support url or b64_json response formats", nil)
	}
	// Marshal a copy to preserve native parameters without mutating the request.
	cloned := *req
	cloned.Provider, cloned.User, cloned.Size = "", "", ""
	cloned.ResponseFormat = format
	body, err := json.Marshal(cloned)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax image request", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax image parameters", err)
	}
	if req.Size != "" && req.Size != "auto" {
		if fields["width"] != nil || fields["height"] != nil || fields["aspect_ratio"] != nil {
			return nil, core.NewInvalidRequestError("size cannot be combined with width, height, or aspect_ratio", nil)
		}
		parts := strings.Split(req.Size, "x")
		if len(parts) != 2 {
			return nil, core.NewInvalidRequestError("size must be WIDTHxHEIGHT or auto", nil)
		}
		width, widthErr := strconv.Atoi(parts[0])
		height, heightErr := strconv.Atoi(parts[1])
		if widthErr != nil || heightErr != nil || width < 512 || width > 2048 || height < 512 || height > 2048 || width%8 != 0 || height%8 != 0 {
			return nil, core.NewInvalidRequestError("image dimensions must be multiples of 8 between 512 and 2048", nil)
		}
		fields["width"] = json.RawMessage(strconv.Itoa(width))
		fields["height"] = json.RawMessage(strconv.Itoa(height))
	}
	body, err = json.Marshal(fields)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax image parameters", err)
	}
	upstream, err := p.Passthrough(ctx, &core.PassthroughRequest{
		Method: http.MethodPost, Endpoint: "/image_generation",
		Body: io.NopCloser(bytes.NewReader(body)), Headers: http.Header{"Content-Type": {"application/json"}},
	})
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
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return nil, core.ParseProviderError("minimax", upstream.StatusCode, responseBody, nil)
	}
	var response struct {
		Data struct {
			URLs   []string `json:"image_urls"`
			Base64 []string `json:"image_base64"`
		} `json:"data"`
		BaseResponse *struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to parse image response", err)
	}
	if response.BaseResponse == nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "image response contains no status", nil)
	}
	if code := response.BaseResponse.StatusCode; code != 0 {
		return nil, statusError("image", code, response.BaseResponse.StatusMsg, responseBody)
	}
	result := &core.ImageGenerationResponse{Created: time.Now().Unix()}
	if format == "url" {
		for _, value := range response.Data.URLs {
			if strings.TrimSpace(value) != "" {
				result.Data = append(result.Data, core.ImageData{URL: value})
			}
		}
	} else {
		for _, value := range response.Data.Base64 {
			if strings.TrimSpace(value) != "" {
				result.Data = append(result.Data, core.ImageData{B64JSON: value})
			}
		}
	}
	if len(result.Data) == 0 {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "image response contains no images", nil)
	}
	return result, nil
}
