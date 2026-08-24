package gemini

import (
	"context"
	"net/http"
	"net/url"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Native Gemini embeddings (models/{model}:batchEmbedContents). One batch call
// covers single- and multi-input OpenAI requests alike.
type geminiEmbedContentRequest struct {
	Model                string        `json:"model"`
	Content              geminiContent `json:"content"`
	OutputDimensionality *int          `json:"outputDimensionality,omitempty"`
}

type geminiBatchEmbedContentsRequest struct {
	Requests []geminiEmbedContentRequest `json:"requests"`
}

type geminiBatchEmbedContentsResponse struct {
	Embeddings []geminiEmbeddingValues `json:"embeddings"`
}

type geminiEmbeddingValues struct {
	Values []float64 `json:"values"`
}

func nativeBatchEmbedEndpoint(model string) string {
	return "/models/" + url.PathEscape(normalizeGeminiModelID(model)) + ":batchEmbedContents"
}

// nativeEmbeddings serves an OpenAI embeddings request through Gemini's native
// batchEmbedContents API. The native response carries no token usage, so the
// OpenAI usage block is returned zeroed.
func (p *Provider) nativeEmbeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	inputs, err := providers.EmbeddingInputs(req.Input)
	if err != nil {
		return nil, err
	}
	model := "models/" + normalizeGeminiModelID(req.Model)
	body := geminiBatchEmbedContentsRequest{
		Requests: make([]geminiEmbedContentRequest, 0, len(inputs)),
	}
	for _, input := range inputs {
		request := geminiEmbedContentRequest{
			Model:   model,
			Content: geminiContent{Parts: []geminiPart{{Text: input}}},
		}
		if req.Dimensions != nil && *req.Dimensions > 0 {
			request.OutputDimensionality = req.Dimensions
		}
		body.Requests = append(body.Requests, request)
	}

	var resp geminiBatchEmbedContentsResponse
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  nativeBatchEmbedEndpoint(req.Model),
		Operation: llmclient.OperationEmbeddings,
		Model:     req.Model,
		Body:      &body,
	}, &resp)
	if err != nil {
		return nil, err
	}

	out := &core.EmbeddingResponse{
		Object:   "list",
		Data:     make([]core.EmbeddingData, 0, len(resp.Embeddings)),
		Model:    req.Model,
		Provider: p.responseProviderName(),
	}
	for i, embedding := range resp.Embeddings {
		encoded, err := providers.EncodeEmbeddingValues(embedding.Values, req.EncodingFormat)
		if err != nil {
			return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "failed to encode Gemini embedding response", err)
		}
		out.Data = append(out.Data, core.EmbeddingData{
			Object:    "embedding",
			Embedding: encoded,
			Index:     i,
		})
	}
	return out, nil
}
