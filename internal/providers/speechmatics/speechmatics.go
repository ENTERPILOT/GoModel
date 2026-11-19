// Package speechmatics provides Speechmatics speech-to-text integration for the
// LLM gateway. Speechmatics is a voice-only provider: GoModel serves its Batch
// API behind /v1/audio/transcriptions, and it has no chat, Responses, or
// embeddings API.
package speechmatics

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// defaultBaseURL is the EU1 Batch endpoint, the one every Speechmatics account
// can use. A job lives in the region that created it, so one provider instance
// talks to one region.
const defaultBaseURL = "https://eu1.asr.api.speechmatics.com/v2"

const (
	// defaultJobTimeout bounds one transcription from upload to transcript. It
	// matches the default HTTP_TIMEOUT other providers' single calls get.
	defaultJobTimeout = 10 * time.Minute
	// Status requests share a per-account limit (50 per second), so polls
	// start a second apart and the gap grows, keeping a long job cheap.
	defaultPollInterval    = time.Second
	defaultMaxPollInterval = 5 * time.Second
	// cancelTimeout bounds the request that terminates an abandoned job.
	cancelTimeout = 10 * time.Second
)

// Registration provides factory registration for the Speechmatics provider.
var Registration = providers.Registration{
	Type: "speechmatics",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Speechmatics' Batch transcription API behind the
// OpenAI-compatible /v1/audio/transcriptions endpoint, plus native
// passthrough. Chat, Responses, embeddings, and text-to-speech return
// invalid_request_error.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
	// name is the provider name reported in errors.
	name string

	jobTimeout      time.Duration
	pollInterval    time.Duration
	maxPollInterval time.Duration
}

var _ core.Provider = (*Provider)(nil)
var _ core.AudioProvider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates a Speechmatics provider using the shared resilience and
// observability settings.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		keys:            opts.Keyring(cfg.APIKey),
		name:            opts.ClientName("speechmatics"),
		jobTimeout:      defaultJobTimeout,
		pollInterval:    defaultPollInterval,
		maxPollInterval: defaultMaxPollInterval,
	}
	clientCfg := llmclient.Config{
		ProviderName:   p.name,
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, clientCfg, p.setHeaders)
	return p
}

// SetBaseURL changes the Speechmatics API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.client.SetBaseURL(baseURL)
}

// pinnedKeyContextKey carries the API key a transcription job must keep using:
// a job is only visible to the account that created it, so key rotation must
// not split its create, status, and transcript calls across accounts.
type pinnedKeyContextKey struct{}

func (p *Provider) setHeaders(req *http.Request) {
	key, pinned := req.Context().Value(pinnedKeyContextKey{}).(string)
	if !pinned {
		key = p.keys.NextForContext(req.Context())
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// ChatCompletion reports that Speechmatics has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support chat completions", nil)
}

// StreamChatCompletion reports that Speechmatics has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support chat completions", nil)
}

// Responses reports that Speechmatics has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support the responses API", nil)
}

// StreamResponses reports that Speechmatics has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support the responses API", nil)
}

// Embeddings reports that Speechmatics has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support embeddings", nil)
}

// CreateSpeech reports that text-to-speech is not served. AudioProvider groups
// speech and transcription into one optional capability, so a
// transcription-only provider must still implement this method.
func (p *Provider) CreateSpeech(_ context.Context, _ *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return nil, core.NewInvalidRequestError("speechmatics does not support text-to-speech", nil)
}

// Passthrough forwards a Speechmatics-native request without typed translation.
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

// multilingualModels are the models that detect languages themselves. They
// take the fixed language "multi" and reject language identification ("auto").
var multilingualModels = map[string]bool{
	"melia-1": true,
	"oak-1":   true,
}

// transcriptionModels are the Batch models Speechmatics documents. The API has
// no model listing, so the catalog is fixed; a model configured on the
// provider is sent upstream as is.
var transcriptionModels = []struct{ id, displayName, description string }{
	{"enhanced", "Speechmatics Enhanced", "Highest accuracy for audio in one selected or detected language."},
	{"standard", "Speechmatics Standard", "Faster turnaround for audio in one selected or detected language."},
	{"melia-1", "Speechmatics Melia 1", "Multilingual: transcribes speakers who switch language without a language setting."},
	{"oak-1", "Speechmatics Oak 1", "Multilingual model tuned for healthcare audio."},
}

// ListModels returns the fixed Speechmatics Batch model list.
func (p *Provider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	models := make([]core.Model, 0, len(transcriptionModels))
	for _, model := range transcriptionModels {
		modes := []string{"audio_transcription"}
		models = append(models, core.Model{
			ID:      model.id,
			Object:  "model",
			OwnedBy: "speechmatics",
			Metadata: &core.ModelMetadata{
				DisplayName: model.displayName,
				Description: model.description,
				Modes:       modes,
				Categories:  core.CategoriesForModes(modes),
			},
		})
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}
