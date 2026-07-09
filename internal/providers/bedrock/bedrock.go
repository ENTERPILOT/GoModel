// Package bedrock provides Amazon Bedrock integration for the LLM gateway.
//
// Bedrock is a managed AWS service that exposes foundation models from many
// vendors (Anthropic, Amazon Nova, Meta Llama, Mistral, Cohere, AI21, ...)
// behind a single SigV4-authenticated API. This provider uses the Bedrock
// Runtime Converse / ConverseStream APIs which present a uniform request/
// response shape across model families, and normalizes the result into
// OpenAI-compatible chat completions.
//
// Authentication relies on the AWS SDK's default credential chain — env vars,
// shared config/credentials files, IAM Identity Center, container and
// instance roles. The provider has no API key of its own.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

const providerName = "bedrock"

// Registration provides factory registration for the Amazon Bedrock provider.
var Registration = providers.Registration{
	Type: providerName,
	New:  New,
	Discovery: providers.DiscoveryConfig{
		AllowAPIKeyless: true,
	},
}

// Provider implements core.Provider for Amazon Bedrock.
type Provider struct {
	region    string
	runtime   *bedrockruntime.Client
	control   *bedrock.Client
	configErr error
}

// New constructs a Bedrock provider from the resolved configuration.
//
// BaseURL is interpreted as either an AWS region ("us-east-1") or a fully
// qualified endpoint URL. When empty, the region is resolved from the
// standard AWS environment variables / shared config.
func New(providerCfg providers.ProviderConfig, _ providers.ProviderOptions) core.Provider {
	p := &Provider{}

	region, endpoint := parseBaseURL(providerCfg.BaseURL)
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		p.configErr = fmt.Errorf("load AWS config: %w", err)
		return p
	}
	if region == "" {
		region = awsCfg.Region
	}
	p.region = region

	runtimeOpts := func(o *bedrockruntime.Options) {
		if endpoint != "" {
			o.BaseEndpoint = awssdk.String(runtimePlaneEndpoint(endpoint))
		}
	}
	controlOpts := func(o *bedrock.Options) {
		if endpoint != "" {
			o.BaseEndpoint = awssdk.String(controlPlaneEndpoint(endpoint))
		}
	}

	p.runtime = bedrockruntime.NewFromConfig(awsCfg, runtimeOpts)
	p.control = bedrock.NewFromConfig(awsCfg, controlOpts)
	return p
}

// parseBaseURL splits the operator-provided BaseURL into (region, endpoint).
// Region-only strings ("us-east-1") configure region without a custom endpoint.
// URLs ("https://...") configure an endpoint and try to derive the region from
// the hostname when it follows the standard AWS naming convention.
func parseBaseURL(baseURL string) (region, endpoint string) {
	v := strings.TrimSpace(baseURL)
	if v == "" {
		return "", ""
	}
	if strings.Contains(v, "://") {
		return regionFromHost(v), v
	}
	return v, ""
}

// regionFromHost extracts the region segment from a Bedrock endpoint URL like
// https://bedrock-runtime.us-east-1.amazonaws.com. To avoid mis-extracting
// segments from custom hosts that happen to contain "bedrock." in their name,
// the host must end with ".amazonaws.com".
func regionFromHost(rawURL string) string {
	_, host, ok := strings.Cut(rawURL, "://")
	if !ok {
		return ""
	}
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	if !strings.HasSuffix(host, ".amazonaws.com") {
		return ""
	}
	parts := strings.Split(host, ".")
	for i, part := range parts {
		if (part == "bedrock" || part == "bedrock-runtime") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// controlPlaneEndpoint rewrites a runtime endpoint host into its control-plane
// equivalent (bedrock-runtime.<region>... → bedrock.<region>...). When the
// input is already a control-plane URL, it's returned unchanged. The match is
// anchored to "://bedrock-runtime." (matching the same guard used by
// runtimePlaneEndpoint) to avoid corrupting custom hostnames such as
// https://my-bedrock-runtime.internal.example.com.
func controlPlaneEndpoint(endpoint string) string {
	return strings.Replace(endpoint, "://bedrock-runtime.", "://bedrock.", 1)
}

// runtimePlaneEndpoint rewrites a control-plane endpoint host into its runtime
// equivalent (bedrock.<region>... → bedrock-runtime.<region>...). When the
// input is already a runtime URL, it's returned unchanged. The string match
// looks for "://bedrock." to avoid corrupting custom hostnames like
// "bedrock.internal.example.com".
func runtimePlaneEndpoint(endpoint string) string {
	if strings.Contains(endpoint, "bedrock-runtime.") {
		return endpoint
	}
	return strings.Replace(endpoint, "://bedrock.", "://bedrock-runtime.", 1)
}

func (p *Provider) ready() error {
	if p.configErr == nil {
		return nil
	}
	return core.NewProviderError(providerName, http.StatusBadGateway, "invalid Bedrock provider configuration: "+p.configErr.Error(), p.configErr)
}

// CheckAvailability calls a no-op Bedrock control-plane request to confirm the
// account has reachability and credentials work.
func (p *Provider) CheckAvailability(ctx context.Context) error {
	if err := p.ready(); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := p.control.ListFoundationModels(probeCtx, &bedrock.ListFoundationModelsInput{
		ByInferenceType: bedrocktypes.InferenceTypeOnDemand,
	})
	if err != nil {
		return mapAWSError(err)
	}
	return nil
}

// ListModels returns Bedrock foundation models that support on-demand text
// inference. Operators can add more (e.g. inference profiles or custom model
// ARNs) via the BEDROCK_MODELS env var, which is honored at the registry
// layer.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	out, err := p.control.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{
		ByInferenceType: bedrocktypes.InferenceTypeOnDemand,
	})
	if err != nil {
		return nil, mapAWSError(err)
	}
	models := make([]core.Model, 0, len(out.ModelSummaries))
	for _, m := range out.ModelSummaries {
		if m.ModelId == nil || !supportsTextOutput(m.OutputModalities) {
			continue
		}
		owner := providerName
		if m.ProviderName != nil && *m.ProviderName != "" {
			owner = strings.ToLower(*m.ProviderName)
		}
		models = append(models, core.Model{
			ID:      *m.ModelId,
			Object:  "model",
			OwnedBy: owner,
			Created: time.Now().Unix(),
		})
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   models,
	}, nil
}

func supportsTextOutput(modalities []bedrocktypes.ModelModality) bool {
	return slices.Contains(modalities, bedrocktypes.ModelModalityText)
}

// Embeddings returns an error: Bedrock embedding models use a different code
// path (InvokeModel with model-specific bodies) which is not yet implemented.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("bedrock embeddings are not yet supported by gomodel", nil)
}

// Responses adapts the OpenAI Responses API onto Converse via the shared chat
// bridge.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses adapts the streaming Responses API onto Converse via the
// shared chat bridge.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.StreamResponsesViaChat(ctx, p, req, providerName)
}

// mapAWSError converts an AWS SDK error into a gateway error preserving HTTP
// status when possible.
func mapAWSError(err error) error {
	if err == nil {
		return nil
	}

	type httpStatus interface{ HTTPStatusCode() int }
	var hs httpStatus
	status := http.StatusBadGateway
	if errors.As(err, &hs) {
		if code := hs.HTTPStatusCode(); code > 0 {
			status = code
		}
	}

	type apiErr interface {
		error
		ErrorCode() string
		ErrorMessage() string
	}
	var ae apiErr
	message := err.Error()
	if errors.As(err, &ae) {
		if msg := strings.TrimSpace(ae.ErrorMessage()); msg != "" {
			message = fmt.Sprintf("%s: %s", ae.ErrorCode(), msg)
		}
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return core.NewAuthenticationError(providerName, message)
	case http.StatusTooManyRequests:
		return core.NewRateLimitError(providerName, message)
	case http.StatusBadRequest:
		return core.NewInvalidRequestErrorWithStatus(http.StatusBadRequest, message, err)
	case http.StatusNotFound:
		ge := core.NewNotFoundError(message)
		ge.Provider = providerName
		ge.Err = err
		return ge
	}
	return core.NewProviderError(providerName, status, message, err)
}

var _ core.Provider = (*Provider)(nil)
