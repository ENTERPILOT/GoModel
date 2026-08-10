package cohere

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// Embeddings translates OpenAI-compatible embedding requests to Cohere v2.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	upstreamReq, err := toCohereEmbedRequest(req)
	if err != nil {
		return nil, err
	}
	var upstream embedResponse
	if err := p.client.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/v2/embed",
		Operation: llmclient.OperationEmbeddings,
		Model:     req.Model,
		Body:      upstreamReq,
	}, &upstream); err != nil {
		return nil, err
	}
	return fromCohereEmbedResponse(&upstream, req), nil
}

func toCohereEmbedRequest(req *core.EmbeddingRequest) (*embedRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("embedding request is required", nil)
	}
	texts, err := embeddingTexts(req.Input)
	if err != nil {
		return nil, err
	}
	encoding := strings.ToLower(strings.TrimSpace(req.EncodingFormat))
	if encoding == "" {
		encoding = "float"
	}
	if encoding != "float" && encoding != "base64" {
		return nil, core.NewInvalidRequestError("encoding_format must be float or base64", nil)
	}
	inputType := rawString(req.ExtraFields.Lookup("input_type"))
	if inputType == "" {
		inputType = "search_document"
	}
	out := &embedRequest{
		Texts:           texts,
		Model:           req.Model,
		InputType:       inputType,
		EmbeddingTypes:  []string{encoding},
		OutputDimension: req.Dimensions,
		MaxTokens:       req.ExtraFields.Lookup("max_tokens"),
		Truncate:        req.ExtraFields.Lookup("truncate"),
		Priority:        req.ExtraFields.Lookup("priority"),
	}
	return out, nil
}

func embeddingTexts(input any) ([]string, error) {
	switch value := input.(type) {
	case string:
		return []string{value}, nil
	case []string:
		return append([]string(nil), value...), nil
	case []any:
		texts := make([]string, len(value))
		for i, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("input[%d] must be a string for Cohere embeddings", i), nil)
			}
			texts[i] = text
		}
		return texts, nil
	default:
		return nil, core.NewInvalidRequestError("input must be a string or array of strings for Cohere embeddings", nil)
	}
}

func fromCohereEmbedResponse(resp *embedResponse, req *core.EmbeddingRequest) *core.EmbeddingResponse {
	base64Format := strings.EqualFold(strings.TrimSpace(req.EncodingFormat), "base64")
	count := len(resp.Embeddings.Float)
	if base64Format {
		count = len(resp.Embeddings.Base64)
	}
	data := make([]core.EmbeddingData, 0, count)
	for i := 0; i < count; i++ {
		var encoded json.RawMessage
		if base64Format {
			encoded, _ = json.Marshal(resp.Embeddings.Base64[i])
		} else {
			encoded, _ = json.Marshal(resp.Embeddings.Float[i])
		}
		data = append(data, core.EmbeddingData{
			Object:    "embedding",
			Embedding: encoded,
			Index:     i,
		})
	}
	inputTokens := int(resp.Meta.BilledUnits.InputTokens)
	return &core.EmbeddingResponse{
		Object:   "list",
		Data:     data,
		Model:    req.Model,
		Provider: "cohere",
		Usage: core.EmbeddingUsage{
			PromptTokens: inputTokens,
			TotalTokens:  inputTokens,
		},
	}
}
