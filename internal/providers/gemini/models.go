package gemini

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// geminiModel represents a model in Gemini's native API response
type geminiModel struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName"`
	Description      string   `json:"description"`
	SupportedMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit  int      `json:"inputTokenLimit"`
	OutputTokenLimit int      `json:"outputTokenLimit"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	TopK             *int     `json:"topK,omitempty"`
	Thinking         bool     `json:"thinking"`
}

// geminiModelsResponse represents one page of the native Gemini models list.
// The API pages at 50 models by default and hands out nextPageToken until the
// last page, which omits it.
type geminiModelsResponse struct {
	Models          []geminiModel `json:"models"`
	PublisherModels []geminiModel `json:"publisherModels"`
	NextPageToken   string        `json:"nextPageToken"`
}

// maxModelListPages bounds the pagination loop so a server that keeps handing
// out tokens cannot stall discovery. At 50 models a page it covers 1000
// models, far beyond Google's catalog.
const maxModelListPages = 20

func geminiModelSupportedMethods(modelID string, methods []string) (supportsGenerate, supportsEmbed, supportsImage bool) {
	normalized := normalizeGeminiModelID(modelID)
	if len(methods) == 0 {
		isEmbedding := strings.HasPrefix(normalized, "text-embedding-") || strings.HasPrefix(normalized, "gemini-embedding-")
		return strings.HasPrefix(normalized, "gemini-") && !isEmbedding, isEmbedding,
			strings.HasPrefix(normalized, "imagen-")
	}
	supportsGenerate = slices.Contains(methods, "generateContent") || slices.Contains(methods, "streamGenerateContent")
	// Imagen models list only the predict method; Gemini image models carry
	// generateContent, so their image capability is inferred from the ID.
	supportsImage = (strings.HasPrefix(normalized, "imagen-") && slices.Contains(methods, "predict")) ||
		(supportsGenerate && isGeminiImageModelID(normalized))
	return supportsGenerate, slices.Contains(methods, "embedContent"), supportsImage
}

// isGeminiImageModelID recognizes generateContent models with image output
// (gemini-2.5-flash-image, gemini-2.0-flash-preview-image-generation,
// gemini-3-pro-image-preview, ...) by the -image marker Google uses in their
// IDs. This only seeds discovery metadata; registry enrichment overrides it.
func isGeminiImageModelID(normalized string) bool {
	return strings.HasPrefix(normalized, "gemini-") &&
		(strings.Contains(normalized, "-image-") || strings.HasSuffix(normalized, "-image"))
}

// geminiDiscoveredMetadata stamps what the native listing says about a model:
// modes/categories from supportedGenerationMethods (so embedding models are
// classified even when the remote model registry has no entry), the display
// name and description, the token limits, and thinking support. Registry
// enrichment merges its entry underneath field by field, and operator config
// on top, so a field the listing reports always wins over the catalog.
func geminiDiscoveredMetadata(gm geminiModel, supportsGenerate, supportsEmbed, supportsImage bool) *core.ModelMetadata {
	modes := make([]string, 0, 3)
	if supportsGenerate {
		modes = append(modes, "chat")
	}
	if supportsEmbed {
		modes = append(modes, "embedding")
	}
	if supportsImage {
		modes = append(modes, "image_generation")
		if supportsGenerate {
			// Only generateContent image models accept input images to edit;
			// Imagen predict models generate from text alone.
			modes = append(modes, "image_edit")
		}
	}
	metadata := &core.ModelMetadata{
		DisplayName: strings.TrimSpace(gm.DisplayName),
		Description: strings.TrimSpace(gm.Description),
	}
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if gm.InputTokenLimit > 0 {
		metadata.ContextWindow = new(gm.InputTokenLimit)
	}
	// Embedding models report a nominal output limit of 1; only generation
	// models have an output limit worth surfacing.
	if gm.OutputTokenLimit > 0 && supportsGenerate {
		metadata.MaxOutputTokens = new(gm.OutputTokenLimit)
	}
	if gm.Thinking {
		metadata.Capabilities = map[string]bool{"reasoning": true}
	}
	if len(modes) == 0 && metadata.DisplayName == "" && metadata.Description == "" &&
		metadata.ContextWindow == nil && metadata.MaxOutputTokens == nil && metadata.Capabilities == nil {
		return nil
	}
	return metadata
}

// ListModels retrieves the list of available models from Gemini, following
// the native listing's page tokens so models past the first page of 50 are
// not dropped.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	modelsClient := p.modelsClient
	if modelsClient == nil {
		modelsClient = p.nativeClient
	}
	rawResp, err := modelsClient.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	// Preferred path: native Gemini models response.
	// If the payload contains an explicit "models" field with an empty array,
	// return an empty list instead of falling through to fallback parsing.
	var nativeProbe struct {
		Models          json.RawMessage `json:"models"`
		PublisherModels json.RawMessage `json:"publisherModels"`
	}
	if err := json.Unmarshal(rawResp.Body, &nativeProbe); err == nil && (nativeProbe.Models != nil || nativeProbe.PublisherModels != nil) {
		var geminiResp geminiModelsResponse
		if err := json.Unmarshal(rawResp.Body, &geminiResp); err != nil {
			return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "failed to parse native Gemini models response", err)
		}
		modelEntries := append(geminiResp.Models, geminiResp.PublisherModels...)
		// The first page decides the response shape; later pages are native
		// by construction, so they are fetched and appended here.
		for token, page := geminiResp.NextPageToken, 1; token != "" && page < maxModelListPages; page++ {
			var next geminiModelsResponse
			if err := modelsClient.Do(ctx, llmclient.Request{
				Method:   http.MethodGet,
				Endpoint: "/models?pageToken=" + url.QueryEscape(token),
			}, &next); err != nil {
				return nil, err
			}
			modelEntries = append(modelEntries, next.Models...)
			modelEntries = append(modelEntries, next.PublisherModels...)
			token = next.NextPageToken
		}
		if len(modelEntries) == 0 {
			return &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{},
			}, nil
		}

		models := make([]core.Model, 0, len(modelEntries))

		for _, gm := range modelEntries {
			modelID := displayModelIDFromGemini(gm.Name, p.backend)

			supportsGenerate, supportsEmbed, supportsImage := geminiModelSupportedMethods(modelID, gm.SupportedMethods)

			if (supportsGenerate || supportsEmbed || supportsImage) && isGeminiExposedModel(modelID) {
				models = append(models, core.Model{
					ID:       modelID,
					Object:   "model",
					OwnedBy:  "google",
					Created:  now,
					Metadata: geminiDiscoveredMetadata(gm, supportsGenerate, supportsEmbed, supportsImage),
				})
			}
		}

		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	// Fallback path: OpenAI-compatible models list.
	var openAIResp core.ModelsResponse
	if err := json.Unmarshal(rawResp.Body, &openAIResp); err == nil && openAIResp.Object == "list" {
		models := make([]core.Model, 0, len(openAIResp.Data))
		for _, m := range openAIResp.Data {
			modelID := displayModelIDFromGemini(m.ID, p.backend)
			isOpenAICompatModel := isGeminiExposedModel(modelID)
			if !isOpenAICompatModel {
				continue
			}
			models = append(models, core.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: "google",
				Created: now,
			})
		}
		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	responsePreview := string(rawResp.Body)
	if len(responsePreview) > 512 {
		responsePreview = responsePreview[:512] + "...(truncated)"
	}
	return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "unexpected Gemini models response format", fmt.Errorf("models response body: %s", responsePreview))
}
