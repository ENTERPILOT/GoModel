package oracle

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://example.invalid"

var Registration = providers.Registration{
	Type: "oracle",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		RequireBaseURL: true,
	},
}

type Provider struct {
	compat           *openai.CompatibleProvider
	configuredModels []string
}

func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		compat: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "oracle",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
		configuredModels: normalizeConfiguredModels(opts.Models),
	}
}

func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks, models []string) *Provider {
	return &Provider{
		compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "oracle",
			BaseURL:      defaultBaseURL,
			SetHeaders:   setHeaders,
		}),
		configuredModels: normalizeConfiguredModels(models),
	}
}

func (p *Provider) SetBaseURL(baseURL string) {
	p.compat.SetBaseURL(baseURL)
}

func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	resp, err := p.compat.ListModels(ctx)
	if len(p.configuredModels) == 0 {
		if err != nil {
			return nil, core.NewProviderError(
				"oracle",
				http.StatusBadGateway,
				"oracle ListModels failed: "+err.Error()+"; set ORACLE_MODELS or add providers.<name>.models in config.yaml to use Oracle when upstream /models is unavailable",
				err,
			)
		}
		return resp, nil
	}
	if err != nil {
		slog.Warn("oracle upstream ListModels failed, using configured models fallback",
			"error", err,
			"configured_models", len(p.configuredModels),
		)
	}

	byID := make(map[string]core.Model, len(p.configuredModels))
	if err == nil && resp != nil {
		for _, model := range resp.Data {
			byID[strings.TrimSpace(model.ID)] = model
		}
	}

	data := make([]core.Model, 0, len(p.configuredModels))
	for _, modelID := range p.configuredModels {
		model, ok := byID[modelID]
		if !ok {
			model = core.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: "oracle",
			}
		} else {
			if strings.TrimSpace(model.Object) == "" {
				model.Object = "model"
			}
			if strings.TrimSpace(model.OwnedBy) == "" {
				model.OwnedBy = "oracle"
			}
		}
		data = append(data, model)
	}

	return &core.ModelsResponse{
		Object: "list",
		Data:   data,
	}, nil
}

func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compat.Responses(ctx, req)
}

func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compat.StreamResponses(ctx, req)
}

func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("oracle does not support embeddings", nil)
}

func setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func normalizeConfiguredModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
