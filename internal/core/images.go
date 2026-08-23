package core

import (
	"strings"

	"github.com/goccy/go-json"
)

// ImageGenerationRequest is an OpenAI-compatible POST /v1/images/generations
// request. Only the fields the gateway needs to route, validate, and audit are
// typed; every other field (quality, size, style, background, output_format,
// moderation, ...) is preserved verbatim in ExtraFields and forwarded upstream
// so new provider parameters work without a gateway change.
type ImageGenerationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	User           string `json:"user,omitempty"`
	// Stream is typed only so the gateway can reject it: streaming image
	// generation returns server-sent events, which this endpoint does not relay.
	Stream bool `json:"stream,omitempty"`

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string `json:"provider,omitempty"`

	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

// ImageCount returns the number of images the request asks for, defaulting to
// one when n is omitted (OpenAI's default).
func (r *ImageGenerationRequest) ImageCount() int {
	if r == nil || r.N == nil || *r.N < 1 {
		return 1
	}
	return *r.N
}

func (r *ImageGenerationRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              *int   `json:"n,omitempty"`
		ResponseFormat string `json:"response_format,omitempty"`
		Size           string `json:"size,omitempty"`
		Quality        string `json:"quality,omitempty"`
		User           string `json:"user,omitempty"`
		Stream         bool   `json:"stream,omitempty"`
		Provider       string `json:"provider,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"model",
		"prompt",
		"n",
		"response_format",
		"size",
		"quality",
		"user",
		"stream",
		"provider",
	)
	if err != nil {
		return err
	}

	r.Model = raw.Model
	r.Prompt = raw.Prompt
	r.N = raw.N
	r.ResponseFormat = raw.ResponseFormat
	r.Size = raw.Size
	r.Quality = raw.Quality
	r.User = raw.User
	r.Stream = raw.Stream
	r.Provider = raw.Provider
	r.ExtraFields = extraFields
	return nil
}

func (r ImageGenerationRequest) MarshalJSON() ([]byte, error) {
	// alias inherits the fields and tags but drops MarshalJSON so json.Marshal
	// does not recurse; ExtraFields (json:"-") is merged separately.
	type alias ImageGenerationRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

// ImageGenerationResponse is the OpenAI-compatible images response envelope.
// Providers that report output parameters (gpt-image-1 echoes background,
// output_format, quality and size) or token usage have them passed through;
// providers that do not simply omit them.
type ImageGenerationResponse struct {
	Created      int64       `json:"created"`
	Data         []ImageData `json:"data"`
	Background   string      `json:"background,omitempty"`
	OutputFormat string      `json:"output_format,omitempty"`
	Quality      string      `json:"quality,omitempty"`
	Size         string      `json:"size,omitempty"`
	Usage        *ImageUsage `json:"usage,omitempty"`

	// Provider is a gateway addition, stamped like every other routed response,
	// so clients can tell which provider served the request.
	Provider string `json:"provider,omitempty"`
}

// ImageData is one generated image: either a hosted URL or inline base64
// bytes, depending on the model and response_format.
type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImageUsage is the token usage block returned by token-billed image models
// (gpt-image-1). DALL·E models omit it entirely.
type ImageUsage struct {
	InputTokens        int                `json:"input_tokens"`
	OutputTokens       int                `json:"output_tokens"`
	TotalTokens        int                `json:"total_tokens"`
	InputTokensDetails *ImageTokenDetails `json:"input_tokens_details,omitempty"`
}

// ImageTokenDetails splits image input tokens between text and image inputs.
type ImageTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	ImageTokens int `json:"image_tokens"`
}

// DecodeImageGenerationRequest decodes a JSON image generation request body.
// The semantic envelope is unused: image responses are not response-cached.
func DecodeImageGenerationRequest(body []byte, _ *WhiteBoxPrompt) (*ImageGenerationRequest, error) {
	var req ImageGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, NewInvalidRequestError("invalid image generation request: "+err.Error(), err)
	}
	return &req, nil
}

// ValidateImageGenerationRequest enforces the fields every image provider
// requires before the request is routed.
func ValidateImageGenerationRequest(req *ImageGenerationRequest) error {
	if req == nil {
		return NewInvalidRequestError("image generation request is required", nil)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return NewInvalidRequestError("prompt is required", nil)
	}
	if req.N != nil && *req.N < 1 {
		return NewInvalidRequestError("n must be at least 1", nil)
	}
	if req.Stream {
		return NewInvalidRequestError("streaming image generation is not supported; omit stream or set it to false", nil)
	}
	return nil
}
