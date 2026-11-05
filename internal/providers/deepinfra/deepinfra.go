// Package deepinfra provides DeepInfra integration for the LLM gateway.
package deepinfra

import (
	"context"
	"io"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.deepinfra.com/v1/openai"

// Registration provides factory registration for the DeepInfra provider.
var Registration = providers.Registration{
	Type: "deepinfra",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements DeepInfra's OpenAI-compatible API. Chat completions,
// embeddings, and passthrough go through the shared chat-centric adapter;
// Responses is forwarded to DeepInfra's native /responses endpoint, except
// requests setting "text", which are translated through chat completions; image
// generation and edits, speech, transcription, translation, files, and
// batches use the matching OpenAI routes. DeepInfra's Responses endpoint is
// stateless, so no response lifecycle (retrieve, cancel, delete) is exposed:
// the gateway replays previous_response_id history itself when it has a
// response store.
type Provider struct {
	*openai.ChatCompatible
	*openai.BatchSurface
	*openai.FileSurface
}

var (
	_ core.Provider                 = (*Provider)(nil)
	_ core.PassthroughProvider      = (*Provider)(nil)
	_ core.ImageProvider            = (*Provider)(nil)
	_ core.ImageEditProvider        = (*Provider)(nil)
	_ core.AudioProvider            = (*Provider)(nil)
	_ core.AudioTranslationProvider = (*Provider)(nil)
	_ core.NativeBatchProvider      = (*Provider)(nil)
	_ core.NativeFileProvider       = (*Provider)(nil)
)

// New creates a new DeepInfra provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	chat := openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "deepinfra",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})
	return &Provider{
		ChatCompatible: chat,
		BatchSurface:   openai.NewBatchSurface(chat.Compatible()),
		FileSurface:    openai.NewFileSurface(chat.Compatible()),
	}
}

// Responses forwards a Responses API request to DeepInfra's native endpoint,
// or through chat completions when the native endpoint cannot serve it (see
// needsChatTranslation).
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if needsChatTranslation(req) {
		return providers.ResponsesViaChat(ctx, p, req, "deepinfra")
	}
	return p.Compatible().Responses(ctx, req)
}

// StreamResponses streams a Responses API request the same way Responses routes it.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if needsChatTranslation(req) {
		return providers.StreamResponsesViaChat(ctx, p, req, "deepinfra")
	}
	return p.Compatible().StreamResponses(ctx, req)
}

// needsChatTranslation reports whether a Responses request sets "text":
// DeepInfra's native endpoint fails every text.format (it builds a
// response_format without a type and answers 500) and rejects text.verbosity,
// while chat completions honors response_format, including json_schema.
func needsChatTranslation(req *core.ResponsesRequest) bool {
	return req != nil && req.Text != nil
}

// CreateImage generates images through DeepInfra's /images/generations endpoint.
func (p *Provider) CreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	return p.Compatible().CreateImage(ctx, req)
}

// CreateImageEdit edits images through DeepInfra's /images/edits endpoint.
func (p *Provider) CreateImageEdit(ctx context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	return p.Compatible().CreateImageEdit(ctx, req)
}

// CreateSpeech synthesizes speech through DeepInfra's /audio/speech endpoint.
// DeepInfra answers WAV when response_format is omitted, so the OpenAI
// default ("mp3") is sent explicitly.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req != nil && strings.TrimSpace(req.ResponseFormat) == "" {
		withFormat := *req
		withFormat.ResponseFormat = "mp3"
		req = &withFormat
	}
	return p.Compatible().CreateSpeech(ctx, req)
}

// CreateTranscription transcribes audio through DeepInfra's /audio/transcriptions endpoint.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return p.Compatible().CreateTranscription(ctx, req)
}

// CreateTranslation translates audio into English through DeepInfra's /audio/translations endpoint.
func (p *Provider) CreateTranslation(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return p.Compatible().CreateTranslation(ctx, req)
}
