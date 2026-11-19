// Package assemblyai provides AssemblyAI speech-to-text integration for the
// LLM gateway. AssemblyAI is a voice provider: this adapter serves its
// pre-recorded transcription API behind /v1/audio/transcriptions and has no
// chat, Responses, or embeddings API to route to.
package assemblyai

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// defaultBaseURL is AssemblyAI's US endpoint. The EU endpoint is
// https://api.eu.assemblyai.com.
const defaultBaseURL = "https://api.assemblyai.com"

const (
	// defaultPollInterval is the first wait between status checks of one
	// transcription job. Later waits grow to three times this, the interval
	// AssemblyAI's own examples poll at, which stays far below its limit of 60
	// status checks per minute for one transcript.
	defaultPollInterval = time.Second
	// defaultJobTimeout bounds how long one request waits for its job, matching
	// the gateway's default upstream request timeout.
	defaultJobTimeout = 10 * time.Minute
	// deleteTimeout bounds the cleanup call that runs after the job, on a
	// context of its own so a client that already disconnected still gets its
	// audio removed.
	deleteTimeout = 10 * time.Second
)

// Registration provides factory registration for the AssemblyAI provider.
var Registration = providers.Registration{
	Type: "assemblyai",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements AssemblyAI's pre-recorded speech-to-text API behind the
// OpenAI-compatible transcription endpoint, plus native passthrough. Chat,
// Responses, embeddings, and text-to-speech return invalid_request_error.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
	// pollInterval and jobTimeout pace and bound the wait for a transcription
	// job. They are fields so tests can shorten them.
	pollInterval time.Duration
	jobTimeout   time.Duration
}

var _ core.Provider = (*Provider)(nil)
var _ core.AudioProvider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates an AssemblyAI provider using the shared resilience and
// observability settings.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		keys:         opts.Keyring(cfg.APIKey),
		pollInterval: defaultPollInterval,
		jobTimeout:   defaultJobTimeout,
	}
	clientCfg := llmclient.Config{
		ProviderName:   opts.ClientName("assemblyai"),
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, clientCfg, p.setHeaders)
	return p
}

// SetBaseURL changes the AssemblyAI API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.client.SetBaseURL(baseURL)
}

// pinnedKeyContextKey carries the API key a transcription job must keep
// using, so key rotation cannot split its requests across accounts.
type pinnedKeyContextKey struct{}

// setHeaders sends the API key as the bare Authorization value: AssemblyAI
// rejects a Bearer prefix.
func (p *Provider) setHeaders(req *http.Request) {
	key, pinned := req.Context().Value(pinnedKeyContextKey{}).(string)
	if !pinned {
		key = p.keys.NextForContext(req.Context())
	}
	req.Header.Set("Authorization", key)
}

// ChatCompletion reports that AssemblyAI has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support chat completions", nil)
}

// StreamChatCompletion reports that AssemblyAI has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support chat completions", nil)
}

// Responses reports that AssemblyAI has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support the responses API", nil)
}

// StreamResponses reports that AssemblyAI has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support the responses API", nil)
}

// Embeddings reports that AssemblyAI has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support embeddings", nil)
}

// Passthrough forwards an AssemblyAI-native request without typed translation.
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

// transcriptionModels lists AssemblyAI's pre-recorded speech models.
// AssemblyAI has no model listing endpoint, so the list is fixed; the ids are
// the values its speech_models request parameter takes.
var transcriptionModels = []core.Model{
	transcriptionModel("assemblyai-stt-3-7", "AssemblyAI-STT-3.7"),
	transcriptionModel("universal-2", "Universal-2"),
}

func transcriptionModel(id, displayName string) core.Model {
	modes := []string{"audio_transcription"}
	return core.Model{
		ID:      id,
		Object:  "model",
		OwnedBy: "assemblyai",
		Metadata: &core.ModelMetadata{
			DisplayName: displayName,
			Modes:       modes,
			Categories:  core.CategoriesForModes(modes),
		},
	}
}

// ListModels returns the fixed speech model list without calling AssemblyAI.
func (p *Provider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list", Data: append([]core.Model{}, transcriptionModels...)}, nil
}
