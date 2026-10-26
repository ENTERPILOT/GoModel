// Package awscfg loads aws-sdk-go-v2 configuration for the AWS-backed
// providers so their API calls share the gateway's outbound transport.
package awscfg

import (
	"context"
	"net/http"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

// LoadConfig loads the default AWS configuration with client (nil selects
// the shared default) as the HTTP client of the service clients and of the
// credential exchanges with AWS endpoints, such as STS and SSO, so the
// provider's outbound proxy and the gateway's TLS trust apply to them.
//
// The EC2 instance metadata (IMDS) credential lookup keeps the SDK's own
// client. Its sub-second dial and response-header timeouts let the chain fail
// fast off EC2 (a shared client would wait seconds for the link-local
// endpoint), and that endpoint must not go through an outbound proxy.
func LoadConfig(ctx context.Context, client *http.Client, optFns ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
	// The SDK-default pass resolves the IMDS settings (endpoint, endpoint
	// mode, disable flag) from the environment and shared config exactly as
	// the SDK would. It reads local configuration only; nothing is fetched.
	sdkDefaults, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return aws.Config{}, err
	}
	metadata := imds.NewFromConfig(sdkDefaults)

	opts := append(slices.Clone(optFns),
		awsconfig.WithHTTPClient(httpclient.NewAWSSDKClient(client)),
		awsconfig.WithEC2RoleCredentialOptions(func(o *ec2rolecreds.Options) {
			o.Client = metadata
		}),
	)
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}
