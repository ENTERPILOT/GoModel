// Package deepgram provides Deepgram voice API integration for the LLM
// gateway. Deepgram is a voice-only provider: it exposes speech-to-text and
// text-to-speech, not chat, Responses, or embeddings.
package deepgram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// defaultBaseURL carries the /v1 prefix every endpoint used here shares
// (/listen, /speak, /models), so /p/deepgram/... passthrough paths resolve the
// same way as for the OpenAI-compatible providers.
const defaultBaseURL = "https://api.deepgram.com/v1"

// Registration provides factory registration for the Deepgram provider.
var Registration = providers.Registration{
	Type: "deepgram",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Deepgram's pre-recorded speech-to-text and text-to-speech
// APIs behind the OpenAI-compatible audio endpoints, plus native passthrough.
// Chat, Responses, and embeddings return invalid_request_error because
// Deepgram has no such endpoints.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
}

var _ core.Provider = (*Provider)(nil)
var _ core.AudioProvider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates a Deepgram provider using the shared resilience and
// observability settings.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(cfg.APIKey)}
	clientCfg := llmclient.Config{
		ProviderName:   opts.ClientName("deepgram"),
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, clientCfg, p.setHeaders)
	return p
}

// SetBaseURL changes the Deepgram API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.client.SetBaseURL(baseURL)
}

// setHeaders sends the API key in Deepgram's "Token" authorization scheme;
// "Bearer" is reserved for its short-lived JWTs.
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Token "+p.keys.NextForContext(req.Context()))
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// ChatCompletion reports that Deepgram has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, core.NewInvalidRequestError("deepgram does not support chat completions", nil)
}

// StreamChatCompletion reports that Deepgram has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("deepgram does not support chat completions", nil)
}

// Responses reports that Deepgram has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, core.NewInvalidRequestError("deepgram does not support the responses API", nil)
}

// StreamResponses reports that Deepgram has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("deepgram does not support the responses API", nil)
}

// Embeddings reports that Deepgram has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("deepgram does not support embeddings", nil)
}

// Passthrough forwards a Deepgram-native request without typed translation.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:        req.Method,
		Endpoint:      providers.PassthroughEndpoint(req.Endpoint),
		RawBodyReader: req.Body,
		Headers:       req.Headers,
	})
	if err != nil {
		return nil, err
	}
	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    providers.CloneHTTPHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

// modelsResponse is the part of GET /models the gateway reads. Deepgram lists
// one entry per model and language, so a canonical name repeats.
type modelsResponse struct {
	STT []struct {
		CanonicalName string `json:"canonical_name"`
		// Batch marks an entry served by the pre-recorded API; the others are
		// for the live websocket API, which the gateway does not translate.
		Batch bool `json:"batch"`
	} `json:"stt"`
	TTS []struct {
		CanonicalName string `json:"canonical_name"`
		Architecture  string `json:"architecture"`
	} `json:"tts"`
}

// ListModels returns Deepgram's pre-recorded speech-to-text models and its
// text-to-speech voice models from GET /models, plus the shorter ids Deepgram
// documents for them: "nova-3" for "nova-3-general", and the voice family
// ("aura-2") that takes the voice in the request's voice field.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, refineError(err)
	}

	models := make([]core.Model, 0, len(upstream.TTS))
	seen := make(map[string]struct{}, len(upstream.TTS))
	add := func(id, mode string) {
		id = strings.TrimSpace(id)
		if _, dup := seen[id]; id == "" || dup {
			return
		}
		seen[id] = struct{}{}
		models = append(models, core.Model{
			ID:      id,
			Object:  "model",
			OwnedBy: "deepgram",
			Metadata: &core.ModelMetadata{
				Modes:      []string{mode},
				Categories: core.CategoriesForModes([]string{mode}),
			},
		})
	}
	for _, model := range upstream.STT {
		if !model.Batch {
			continue
		}
		add(model.CanonicalName, "audio_transcription")
		if family, ok := strings.CutSuffix(strings.TrimSpace(model.CanonicalName), "-general"); ok {
			add(family, "audio_transcription")
		}
	}
	for _, model := range upstream.TTS {
		add(model.CanonicalName, "audio_speech")
		if family := strings.TrimSpace(model.Architecture); isVoiceFamily(family) {
			add(family, "audio_speech")
		}
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

// refineError improves the message on a *core.GatewayError that
// llmclient.Client already built from a non-2xx response. Deepgram reports
// errors as {"err_code", "err_msg"} or {"category", "message", "details"}
// rather than the {"error": {...}} shape the generic parser recognizes, so
// without this the message is the raw JSON body or lacks the detail that says
// what was wrong. Other errors (e.g. transport failures) pass through unchanged.
func refineError(err error) error {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return err
	}
	var body struct {
		ErrMsg  string `json:"err_msg"`
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if json.Unmarshal(gatewayErr.ResponseBody, &body) != nil {
		return err
	}
	message := body.ErrMsg
	if message == "" && body.Message != "" && body.Details != "" {
		message = body.Message + " " + body.Details
	}
	if message == "" {
		return err
	}
	refined := *gatewayErr
	refined.Message = message
	return &refined
}
