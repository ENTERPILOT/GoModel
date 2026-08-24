package gemini

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// CreateImage implements OpenAI-compatible image generation for Gemini.
// In native mode Imagen models are served through models/{model}:predict and
// Gemini image models (gemini-2.5-flash-image, ...) through generateContent
// with image response modalities. In OpenAI-compatible mode the request is
// forwarded to Gemini's /openai/images/generations endpoint unchanged.
func (p *Provider) CreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := core.ValidateImageGenerationRequest(req); err != nil {
		return nil, err
	}
	if !p.useNativeAPI {
		return p.openAICompatibleCreateImage(ctx, req)
	}
	if isImagenModel(req.Model) {
		return p.predictImage(ctx, req)
	}
	return p.generateContentImage(ctx, req)
}

// CreateImageEdit implements OpenAI-compatible image edits for Gemini image
// models through the native generateContent API: uploaded images become
// inline_data parts ahead of the prompt text. Imagen models only generate, and
// the OpenAI-compatible surface has no edits endpoint, so both are rejected
// with a pointer to what works.
func (p *Provider) CreateImageEdit(ctx context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := core.ValidateImageEditRequest(req); err != nil {
		return nil, err
	}
	if !p.useNativeAPI {
		return nil, core.NewInvalidRequestError("image edits require Gemini native API mode; set api_mode: native (or USE_GOOGLE_GEMINI_NATIVE_API=true)", nil)
	}
	if isImagenModel(req.Model) {
		return nil, core.NewInvalidRequestError("model \""+req.Model+"\" does not support image edits; use a Gemini image model such as gemini-2.5-flash-image", nil)
	}
	if req.Mask != nil {
		return nil, core.NewInvalidRequestError("Gemini image edits do not support mask; describe the region to change in the prompt", nil)
	}

	parts := make([]geminiPart, 0, len(req.Images)+1)
	for _, image := range req.Images {
		parts = append(parts, geminiPart{InlineData: &geminiBlob{
			MimeType: imageEditMimeType(image.ContentType),
			Data:     base64.StdEncoding.EncodeToString(image.Data),
		}})
	}
	parts = append(parts, geminiPart{Text: req.Prompt})

	size, _ := req.Field("size")
	body := &geminiGenerateContentRequest{
		Contents:         []geminiContent{{Role: "user", Parts: parts}},
		GenerationConfig: imageGenerationConfig(size, imageEditCount(req), nil),
	}
	return p.doGenerateContentImage(ctx, req.Model, body)
}

func (p *Provider) openAICompatibleCreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if p.backend == geminiBackendVertex {
		if model := vertexOpenAIModelID(req.Model); strings.TrimSpace(model) != "" {
			rewritten := *req
			rewritten.Model = model
			req = &rewritten
		}
	}
	var resp core.ImageGenerationResponse
	err := p.client.Do(ctx, llmclient.Request{
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
	if resp.Provider == "" {
		resp.Provider = p.responseProviderName()
	}
	return &resp, nil
}

// Imagen prediction (models/imagen-*:predict).

type geminiImagePredictRequest struct {
	Instances  []geminiImagePredictInstance `json:"instances"`
	Parameters map[string]any               `json:"parameters,omitempty"`
}

type geminiImagePredictInstance struct {
	Prompt string `json:"prompt"`
}

type geminiImagePredictResponse struct {
	Predictions []geminiImagePrediction `json:"predictions"`
}

type geminiImagePrediction struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded,omitempty"`
	MimeType           string `json:"mimeType,omitempty"`
	Prompt             string `json:"prompt,omitempty"`
	RaiFilteredReason  string `json:"raiFilteredReason,omitempty"`
}

func (p *Provider) predictImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	parameters, err := imagePredictParameters(req)
	if err != nil {
		return nil, err
	}
	body := &geminiImagePredictRequest{
		Instances:  []geminiImagePredictInstance{{Prompt: req.Prompt}},
		Parameters: parameters,
	}
	var predictResp geminiImagePredictResponse
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/models/" + url.PathEscape(normalizeGeminiModelID(req.Model)) + ":predict",
		Model:    req.Model,
		Body:     body,
	}, &predictResp)
	if err != nil {
		return nil, err
	}

	resp := &core.ImageGenerationResponse{
		Created:  time.Now().Unix(),
		Data:     make([]core.ImageData, 0, len(predictResp.Predictions)),
		Provider: p.responseProviderName(),
	}
	filteredReason := ""
	for _, prediction := range predictResp.Predictions {
		if prediction.BytesBase64Encoded == "" {
			if prediction.RaiFilteredReason != "" {
				filteredReason = prediction.RaiFilteredReason
			}
			continue
		}
		resp.Data = append(resp.Data, core.ImageData{
			B64JSON:       prediction.BytesBase64Encoded,
			RevisedPrompt: prediction.Prompt,
		})
	}
	if len(resp.Data) == 0 {
		message := "Gemini returned no images"
		if filteredReason != "" {
			message += ": " + filteredReason
		}
		return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, message, nil)
	}
	return resp, nil
}

// imagePredictParameters maps the OpenAI request onto Imagen predict
// parameters: n becomes sampleCount (explicitly, so OpenAI's default of one
// image wins over Imagen's default of four) and size becomes aspectRatio.
// Unknown request fields are forwarded verbatim so native parameters
// (personGeneration, sampleImageSize, ...) work without a gateway change;
// OpenAI fields with no Imagen equivalent (quality, response_format, user)
// are dropped. Imagen always returns base64 image bytes.
func imagePredictParameters(req *core.ImageGenerationRequest) (map[string]any, error) {
	parameters, err := imageExtraFields(req)
	if err != nil {
		return nil, err
	}
	if _, ok := parameters["sampleCount"]; !ok {
		parameters["sampleCount"] = req.ImageCount()
	}
	if aspectRatio := imageAspectRatio(req.Size); aspectRatio != "" {
		if _, ok := parameters["aspectRatio"]; !ok {
			parameters["aspectRatio"] = aspectRatio
		}
	}
	return parameters, nil
}

// Gemini image model generation (generateContent with image modalities).

func (p *Provider) generateContentImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	extra, err := imageExtraFields(req)
	if err != nil {
		return nil, err
	}
	body := &geminiGenerateContentRequest{
		Contents:         []geminiContent{{Role: "user", Parts: []geminiPart{{Text: req.Prompt}}}},
		GenerationConfig: imageGenerationConfig(req.Size, req.ImageCount(), extra),
	}
	return p.doGenerateContentImage(ctx, req.Model, body)
}

func (p *Provider) doGenerateContentImage(ctx context.Context, model string, body *geminiGenerateContentRequest) (*core.ImageGenerationResponse, error) {
	var geminiResp geminiGenerateContentResponse
	err := p.nativeClient.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  nativeGenerateEndpoint(model),
		Operation: llmclient.OperationGenerateContent,
		Model:     model,
		Body:      body,
	}, &geminiResp)
	if err != nil {
		return nil, err
	}
	return p.imageResponseFromGenerateContent(&geminiResp)
}

// imageGenerationConfig builds the generationConfig for image output. Extra
// request fields merge in verbatim so native options (imageConfig,
// responseModalities overrides, ...) pass through; the explicit mappings only
// fill keys the caller did not set.
func imageGenerationConfig(size string, count int, extra map[string]any) map[string]any {
	config := extra
	if config == nil {
		config = make(map[string]any, 3)
	}
	if _, ok := config["responseModalities"]; !ok {
		// Gemini image models reject image-only output; TEXT must be allowed.
		config["responseModalities"] = []string{"TEXT", "IMAGE"}
	}
	if count > 1 {
		if _, ok := config["candidateCount"]; !ok {
			config["candidateCount"] = count
		}
	}
	aspectRatio := imageAspectRatio(size)
	if aspectRatio == "" {
		return config
	}
	if _, ok := config["imageConfig"]; !ok {
		config["imageConfig"] = map[string]any{"aspectRatio": aspectRatio}
	}
	return config
}

// imageResponseFromGenerateContent flattens candidate parts into the OpenAI
// images envelope: inline image data becomes b64_json entries and any model
// commentary is surfaced as revised_prompt on the first image.
func (p *Provider) imageResponseFromGenerateContent(resp *geminiGenerateContentResponse) (*core.ImageGenerationResponse, error) {
	out := &core.ImageGenerationResponse{
		Created:  time.Now().Unix(),
		Data:     []core.ImageData{},
		Provider: p.responseProviderName(),
	}
	var text strings.Builder
	for _, candidate := range resp.Candidates {
		for _, part := range candidate.Content.Parts {
			if blob := part.inlineData(); blob != nil && blob.Data != "" {
				out.Data = append(out.Data, core.ImageData{B64JSON: blob.Data})
				continue
			}
			if part.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(part.Text)
			}
		}
	}
	if len(out.Data) == 0 {
		return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, imageFailureMessage(resp, text.String()), nil)
	}
	if text.Len() > 0 {
		out.Data[0].RevisedPrompt = text.String()
	}
	if usage := resp.UsageMetadata; usage.TotalTokenCount > 0 {
		out.Usage = &core.ImageUsage{
			InputTokens:  usage.PromptTokenCount,
			OutputTokens: usage.CandidatesTokenCount + usage.ThoughtsTokenCount,
			TotalTokens:  usage.TotalTokenCount,
		}
	}
	return out, nil
}

// imageFailureMessage explains a generateContent response that carried no
// image: a prompt-feedback block reason when present, otherwise the model's
// own text, trimmed to stay log-friendly.
func imageFailureMessage(resp *geminiGenerateContentResponse, text string) string {
	message := "Gemini returned no images"
	var feedback geminiPromptFeedback
	if len(resp.PromptFeedback) > 0 && json.Unmarshal(resp.PromptFeedback, &feedback) == nil && feedback.BlockReason != "" {
		message += ": blocked (" + feedback.BlockReason + ")"
		if feedback.BlockReasonMessage != "" {
			message += " " + feedback.BlockReasonMessage
		}
		return message
	}
	text = strings.TrimSpace(text)
	if text != "" {
		const maxLen = 240
		if len(text) > maxLen {
			text = text[:maxLen] + "..."
		}
		message += ": " + text
	}
	return message
}

// imageExtraFields returns the request fields the gateway does not type,
// keyed as the client sent them. Marshaling the request merges ExtraFields
// back in, so decoding that and dropping the typed keys yields exactly the
// passthrough set.
func imageExtraFields(req *core.ImageGenerationRequest) (map[string]any, error) {
	if req.ExtraFields.IsEmpty() {
		return map[string]any{}, nil
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid image generation request: "+err.Error(), err)
	}
	fields := make(map[string]any)
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, core.NewInvalidRequestError("invalid image generation request: "+err.Error(), err)
	}
	for _, key := range []string{"model", "prompt", "n", "response_format", "size", "quality", "user", "stream", "provider"} {
		delete(fields, key)
	}
	return fields, nil
}

// imageAspectRatio translates an OpenAI size to a Gemini aspect ratio. Ratio
// strings ("16:9") pass through for clients speaking native Gemini already;
// "WxH" pixel sizes reduce to the closest ratio Gemini documents. Anything
// else (including "auto") is omitted so the model default applies.
func imageAspectRatio(size string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" || size == "auto" {
		return ""
	}
	if strings.Contains(size, ":") {
		return size
	}
	width, height, ok := strings.Cut(size, "x")
	w, errW := strconv.Atoi(width)
	h, errH := strconv.Atoi(height)
	if !ok || errW != nil || errH != nil || w <= 0 || h <= 0 {
		return ""
	}
	// The DALL·E landscape/portrait sizes have no exact Gemini ratio; 16:9 and
	// 9:16 are the closest supported shapes.
	switch {
	case w == 1792 && h == 1024:
		return "16:9"
	case w == 1024 && h == 1792:
		return "9:16"
	}
	divisor := gcd(w, h)
	return strconv.Itoa(w/divisor) + ":" + strconv.Itoa(h/divisor)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// isImagenModel reports whether the model belongs to the Imagen family, which
// generates through :predict rather than generateContent.
func isImagenModel(model string) bool {
	return strings.HasPrefix(normalizeGeminiModelID(model), "imagen-")
}

func imageEditMimeType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "image/png"
	}
	if mediaType, _, ok := strings.Cut(contentType, ";"); ok {
		return strings.TrimSpace(mediaType)
	}
	return contentType
}

// imageEditCount reads the n form field; malformed values fall back to one
// image (core validation already rejected non-positive n).
func imageEditCount(req *core.ImageEditRequest) int {
	value, _ := req.Field("n")
	value = strings.TrimSpace(value)
	if value == "" {
		return 1
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 1 {
		return 1
	}
	return count
}

var (
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ImageEditProvider = (*Provider)(nil)
)
