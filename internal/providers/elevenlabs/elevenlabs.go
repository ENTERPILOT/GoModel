// Package elevenlabs provides ElevenLabs voice API integration for the LLM
// gateway. ElevenLabs is a voice-only provider: it exposes text-to-speech and
// speech-to-text, not chat, Responses, or embeddings.
package elevenlabs

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

const defaultBaseURL = "https://api.elevenlabs.io"

// authHeader is ElevenLabs' credential header. It is not an Authorization
// bearer token like most providers.
const authHeader = "xi-api-key"

// Registration provides factory registration for the ElevenLabs provider.
var Registration = providers.Registration{
	Type: "elevenlabs",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements ElevenLabs' native text-to-speech and speech-to-text
// APIs behind the OpenAI-compatible audio endpoints. It does not implement
// core.PassthroughProvider's chat/Responses/Embeddings surface since
// ElevenLabs has no such endpoints.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
}

var _ core.Provider = (*Provider)(nil)
var _ core.AudioProvider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates an ElevenLabs provider using the shared resilience and
// observability settings.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(cfg.APIKey)}
	clientCfg := llmclient.Config{
		ProviderName:   "elevenlabs",
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates an ElevenLabs provider with a custom HTTP client.
func NewWithHTTPClient(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{keys: providers.NewKeyring(apiKey)}
	cfg := llmclient.DefaultConfig("elevenlabs", providers.ResolveBaseURL(baseURL, defaultBaseURL))
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL changes the ElevenLabs API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.client.SetBaseURL(baseURL)
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set(authHeader, p.keys.NextForContext(req.Context()))
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// ChatCompletion reports that ElevenLabs has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, core.NewInvalidRequestError("elevenlabs does not support chat completions", nil)
}

// StreamChatCompletion reports that ElevenLabs has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("elevenlabs does not support chat completions", nil)
}

// Responses reports that ElevenLabs has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, core.NewInvalidRequestError("elevenlabs does not support the responses API", nil)
}

// StreamResponses reports that ElevenLabs has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("elevenlabs does not support the responses API", nil)
}

// Embeddings reports that ElevenLabs has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("elevenlabs does not support embeddings", nil)
}

// Passthrough forwards an ElevenLabs-native request without typed translation.
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

// staticTranscriptionModels lists ElevenLabs' speech-to-text ("Scribe")
// models, which are not included in GET /v1/models (that endpoint only lists
// text-to-speech models). scribe_v2 is current; scribe_v1 remains valid but
// is superseded.
var staticTranscriptionModels = []core.Model{
	{
		ID:      "scribe_v2",
		Object:  "model",
		OwnedBy: "elevenlabs",
		Metadata: &core.ModelMetadata{
			DisplayName: "Scribe v2",
			Modes:       []string{"audio_transcription"},
			Categories:  core.CategoriesForModes([]string{"audio_transcription"}),
		},
	},
	{
		ID:      "scribe_v1",
		Object:  "model",
		OwnedBy: "elevenlabs",
		Metadata: &core.ModelMetadata{
			DisplayName: "Scribe v1",
			Modes:       []string{"audio_transcription"},
			Categories:  core.CategoriesForModes([]string{"audio_transcription"}),
		},
	},
}

type modelInfo struct {
	ModelID           string `json:"model_id"`
	Name              string `json:"name"`
	CanDoTextToSpeech bool   `json:"can_do_text_to_speech"`
	Description       string `json:"description"`
	Languages         []struct {
		LanguageID string `json:"language_id"`
	} `json:"languages"`
}

// ListModels returns ElevenLabs' text-to-speech catalog (from GET /v1/models)
// plus the fixed speech-to-text model list.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream []modelInfo
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models",
	}, &upstream); err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(upstream)+len(staticTranscriptionModels))
	for _, model := range upstream {
		id := strings.TrimSpace(model.ModelID)
		if id == "" || !model.CanDoTextToSpeech {
			continue
		}
		models = append(models, core.Model{
			ID:      id,
			Object:  "model",
			OwnedBy: "elevenlabs",
			Metadata: &core.ModelMetadata{
				DisplayName:  model.Name,
				Description:  model.Description,
				Modes:        []string{"audio_speech"},
				Categories:   core.CategoriesForModes([]string{"audio_speech"}),
				Capabilities: multilingualCapability(model),
			},
		})
	}
	models = append(models, staticTranscriptionModels...)
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

func multilingualCapability(model modelInfo) map[string]bool {
	if len(model.Languages) <= 1 {
		return nil
	}
	return map[string]bool{"multilingual": true}
}
