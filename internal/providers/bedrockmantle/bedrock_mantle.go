// Package bedrockmantle provides direct access to Amazon Bedrock's
// OpenAI-compatible Mantle API. It is deliberately separate from the Bedrock
// Runtime provider: Mantle has different endpoints, authentication options,
// and model capabilities, including Responses-only OpenAI GPT models.
package bedrockmantle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const providerName = "bedrock-mantle"

// Registration provides factory registration for Amazon Bedrock Mantle.
var Registration = providers.Registration{
	Type: providerName,
	New:  New,
	Discovery: providers.DiscoveryConfig{
		AllowAPIKeyless: true,
	},
}

// Provider exposes only the OpenAI-compatible surface documented by Mantle.
// Explicit delegation prevents unsupported OpenAI lifecycle APIs from being
// advertised through interface assertions.
type Provider struct {
	compatible *openai.CompatibleProvider
	configErr  error
}

// New creates a Bedrock Mantle provider. BEDROCK_MANTLE_API_KEY (or
// AWS_BEARER_TOKEN_BEDROCK) uses bearer authentication; without one, the AWS
// SDK default credential chain is used for SigV4 authentication.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	keys := opts.Keyring(cfg.APIKey)
	if keys.Len() == 0 {
		keys = providers.NewKeyring(os.Getenv("AWS_BEARER_TOKEN_BEDROCK"))
	}

	endpoint, err := resolveEndpoint(cfg.BaseURL, cfg.APIMode)
	if err != nil {
		return &Provider{configErr: err}
	}

	var credentials aws.CredentialsProvider
	if keys.Len() == 0 {
		awsCfg, loadErr := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(endpoint.region))
		if loadErr != nil {
			return &Provider{configErr: fmt.Errorf("load AWS config: %w", loadErr)}
		}
		credentials = awsCfg.Credentials
	}

	client := authenticatedClient(httpclient.NewDefaultHTTPClient(), keys, credentials, endpoint.region)
	return newProvider(endpoint, cfg, opts, client)
}

func newProvider(endpoint endpointConfig, cfg providers.ProviderConfig, opts providers.ProviderOptions, client *http.Client) *Provider {
	return &Provider{compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:   providerName,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     client,
		RequestMutator: requestRouter(endpoint.mode),
	})}
}

func (p *Provider) ready() error {
	if p.configErr == nil && p.compatible != nil {
		return nil
	}
	err := p.configErr
	if err == nil {
		err = fmt.Errorf("provider is not initialized")
	}
	return core.NewProviderError(providerName, http.StatusBadGateway, "invalid Bedrock Mantle provider configuration: "+err.Error(), err)
}

func (p *Provider) CheckAvailability(ctx context.Context) error {
	if err := p.ready(); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := p.compatible.ListModels(probeCtx)
	return err
}

func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.compatible.ChatCompletion(ctx, req)
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.compatible.StreamChatCompletion(ctx, req)
}

func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.compatible.ListModels(ctx)
}

func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.compatible.Responses(ctx, req)
}

func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.compatible.StreamResponses(ctx, req)
}

func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return nil, core.NewInvalidRequestError("embeddings are not supported by Bedrock Mantle", nil)
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.AvailabilityChecker = (*Provider)(nil)
)
