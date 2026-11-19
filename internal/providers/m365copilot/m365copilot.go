// Package m365copilot routes chat traffic to Microsoft 365 Copilot through
// the Work IQ REST (Chat) API.
//
// The upstream is not a model endpoint. It is the Copilot assistant itself:
// it keeps conversations on the server, grounds every answer in the signed-in
// user's Microsoft 365 data and the web, and takes one text prompt per turn.
// The provider opens a conversation for each request and sends the last user
// message as the prompt, with the earlier messages as additional context
// (see request.go). What Copilot cannot honour, such as tools, images, or
// JSON output, is rejected rather than dropped.
//
// Reference: https://learn.microsoft.com/microsoft-365/copilot/extensibility/work-iq/rest/overview
package m365copilot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

const (
	providerType = "m365-copilot"
	// defaultBaseURL is the generally available Work IQ REST root. The
	// preview Microsoft Graph Chat API serves the same paths under
	// https://graph.microsoft.com/beta/copilot; M365_COPILOT_BASE_URL moves it.
	defaultBaseURL = "https://workiq.svc.cloud.microsoft/rest"
	// defaultModel is the one model the provider lists. Copilot offers no
	// model choice, so the ID only routes a request here.
	defaultModel = "m365-copilot"
)

// Registration provides factory registration for the Microsoft 365 Copilot
// provider. The "m365-copilot" type derives M365_COPILOT_API_KEY,
// M365_COPILOT_BASE_URL, and M365_COPILOT_MODELS by convention.
var Registration = providers.Registration{
	Type: providerType,
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for the Copilot Chat API.
// Chat completions are served natively and Responses through them; the
// upstream has no embeddings or model-listing endpoint.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
	models []string
	// providerName is the configured instance name when the factory supplied
	// one, the provider type otherwise.
	providerName string
	// timeZone is the location hint sent with every prompt, which the Chat
	// API requires.
	timeZone string
}

var _ core.Provider = (*Provider)(nil)

// New creates a Microsoft 365 Copilot provider. The API key is a delegated
// Microsoft Entra ID access token for the Work IQ API (scope
// api://workiq.svc.cloud.microsoft/WorkIQAgent.Ask); it is sent as-is, with
// no refresh.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		keys:         opts.Keyring(cfg.APIKey),
		models:       opts.Models,
		providerName: opts.ClientName(providerType),
		timeZone:     ianaTimeZone(time.Local),
	}
	if len(p.models) == 0 {
		p.models = []string{defaultModel}
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, llmclient.Config{
		ProviderName:   p.providerName,
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}, nil)
	return p
}

// SetBaseURL overrides the upstream endpoint.
func (p *Provider) SetBaseURL(url string) { p.client.SetBaseURL(url) }

// ianaTimeZone names loc the way the Chat API reads a location hint. The
// gateway's zone is used when TZ names one, UTC otherwise: a zone loaded from
// /etc/localtime or a file path has no IANA name to send.
func ianaTimeZone(loc *time.Location) string {
	name := loc.String()
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") {
		return "UTC"
	}
	return name
}

// ListModels returns the declared inventory. The Chat API has no models
// endpoint and no model choice, so nothing is fetched upstream.
func (p *Provider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	created := time.Now().Unix()
	modes := []string{"chat"}
	models := make([]core.Model, 0, len(p.models))
	for _, id := range p.models {
		models = append(models, core.Model{
			ID:      id,
			Object:  "model",
			OwnedBy: "microsoft",
			Created: created,
			Metadata: &core.ModelMetadata{
				DisplayName: "Microsoft 365 Copilot",
				Modes:       modes,
				Categories:  core.CategoriesForModes(modes),
			},
		})
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

// Responses translates the OpenAI Responses API through Copilot chat.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, p.providerName)
}

// StreamResponses is Responses for streaming requests.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, p.providerName)
}

// Embeddings is unsupported: the Chat API exposes no embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented,
		"m365-copilot serves chat only; embeddings are not available through Microsoft 365 Copilot",
		nil).WithCode("unsupported_provider_operation")
}

// authHeaders picks the request's token once, so the conversation is created
// and continued as the same user even when several keys are configured.
func (p *Provider) authHeaders(ctx context.Context) (http.Header, error) {
	token := strings.TrimSpace(p.keys.NextForContext(ctx))
	if token == "" {
		return nil, core.NewAuthenticationError(p.providerName,
			"missing Microsoft Entra ID access token; set M365_COPILOT_API_KEY to a delegated Work IQ token of the user Copilot answers for")
	}
	return http.Header{"Authorization": []string{"Bearer " + token}}, nil
}
