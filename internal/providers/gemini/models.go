package gemini

import (
	"context"
	"fmt"
	"net/http"
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
}

// geminiModelsResponse represents the native Gemini models list response
type geminiModelsResponse struct {
	Models          []geminiModel `json:"models"`
	PublisherModels []geminiModel `json:"publisherModels"`
}

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

// geminiDiscoveredMetadata stamps modes/categories from the native listing's
// supportedGenerationMethods so embedding models are classified even when the
// remote model registry has no entry (new or preview IDs). Registry enrichment
// replaces this metadata whenever it does have an entry, and operator config
// merges on top, so the discovery stamp is only the lowest-precedence signal.
func geminiDiscoveredMetadata(supportsGenerate, supportsEmbed, supportsImage bool) *core.ModelMetadata {
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
	if len(modes) == 0 {
		return nil
	}
	return &core.ModelMetadata{
		Modes:      modes,
		Categories: core.CategoriesForModes(modes),
	}
}

// ListModels retrieves the list of available models from Gemini
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
					Metadata: geminiDiscoveredMetadata(supportsGenerate, supportsEmbed, supportsImage),
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
