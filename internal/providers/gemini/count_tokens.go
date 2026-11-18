package gemini

import (
	"context"
	"net/http"
	"net/url"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// geminiCountTokensRequest is the body of the Gemini API's countTokens. The
// request rides in generateContentRequest, the form that takes a system
// instruction and tools next to the contents; the bare top-level "contents"
// form does not.
type geminiCountTokensRequest struct {
	GenerateContentRequest geminiCountedRequest `json:"generateContentRequest"`
}

// geminiCountedRequest holds the generateContent members that make up the
// input. The generation config rides along because a response schema is
// billed as input (Gemini 4 counts 100 tokens more for a two-field schema),
// and a cached content the client named is counted with the request that
// reads it. The tool config, safety settings and store flag are left out:
// they do not change the count.
type geminiCountedRequest struct {
	Model             string          `json:"model"`
	SystemInstruction *geminiContent  `json:"system_instruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	GenerationConfig  map[string]any  `json:"generationConfig,omitempty"`
	CachedContent     string          `json:"cachedContent,omitempty"`
}

// CountChatTokens counts a chat request's input tokens with the Gemini API's
// models/{model}:countTokens endpoint, for the request as the native path
// translates it. It only reads: no cached content is created for the count.
// The OpenAI-compatible mode and the Vertex backend have no counting call
// here, and a request the translation refuses cannot be counted, so those
// answer core.ErrMessagesTokenCountUnsupported and the caller estimates
// instead, as it does for an upstream error.
func (p *Provider) CountChatTokens(ctx context.Context, req *core.ChatRequest) (int, error) {
	if err := p.ready(); err != nil {
		return 0, err
	}
	if req == nil || !p.useNativeAPI || p.backend != geminiBackendAIStudio {
		return 0, core.ErrMessagesTokenCountUnsupported
	}
	converted, err := convertChatRequestToGemini(req)
	if err != nil {
		return 0, core.ErrMessagesTokenCountUnsupported
	}
	model := normalizeGeminiModelID(req.Model)
	var resp struct {
		TotalTokens int `json:"totalTokens"`
	}
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/models/" + url.PathEscape(model) + ":countTokens",
		Model:    req.Model,
		Body: geminiCountTokensRequest{GenerateContentRequest: geminiCountedRequest{
			Model:             "models/" + model,
			SystemInstruction: converted.SystemInstruction,
			Contents:          converted.Contents,
			Tools:             converted.Tools,
			GenerationConfig:  converted.GenerationConfig,
			CachedContent:     converted.CachedContent,
		}},
	}, &resp)
	if err != nil {
		return 0, err
	}
	if resp.TotalTokens <= 0 {
		// A reply without a count must not answer "0 tokens".
		return 0, core.ErrMessagesTokenCountUnsupported
	}
	return resp.TotalTokens, nil
}
