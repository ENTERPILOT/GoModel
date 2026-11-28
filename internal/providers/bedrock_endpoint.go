package providers

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// awsRegionExpr is the shape of an AWS region: "us-east-1", "us-gov-west-1".
const awsRegionExpr = `[a-z]{2,}(-[a-z]+)+-[0-9]+`

// bedrockServiceExpr names the Bedrock control plane, runtime and Mantle
// services, with their FIPS variants.
const bedrockServiceExpr = `bedrock(-runtime|-mantle)?(-fips)?`

var awsRegionPattern = regexp.MustCompile(`^` + awsRegionExpr + `$`)

// bedrockHostPattern matches the hosts AWS serves Bedrock on:
// {service}.{region}.amazonaws.com, its China (.amazonaws.com.cn) and
// dual-stack (.api.aws) forms, and the interface VPC endpoint names AWS issues
// for those services under {region}.vpce.amazonaws.com. It is deliberately
// narrower than "any amazonaws.com host": API Gateway and S3 serve names there
// that an AWS customer controls.
var bedrockHostPattern = regexp.MustCompile(
	`^(` + bedrockServiceExpr + `\.` + awsRegionExpr + `\.(amazonaws\.com(\.cn)?|api\.aws)` +
		`|vpce-[a-z0-9-]+\.` + bedrockServiceExpr + `\.` + awsRegionExpr + `\.vpce\.amazonaws\.com(\.cn)?)$`)

// ErrHostAWSCredentialsWithheld is the configuration error of a Bedrock
// provider saved through the admin API whose base URL would take the host's AWS
// credentials off AWS's own Bedrock endpoints.
var ErrHostAWSCredentialsWithheld = errors.New("a base URL that is not an AWS region or an AWS Bedrock endpoint needs an API key saved with the provider: the host's AWS credentials are only sent to AWS's Bedrock endpoints")

// IsBedrockEndpoint reports whether a Bedrock base URL keeps requests on AWS's
// own Bedrock hosts: an AWS region, or an https URL on one of those hosts and
// the default port. Adapters use it to decide whether an endpoint may receive
// a credential taken from the gateway's host rather than from the provider's
// own configuration (see ProviderConfig.Managed).
func IsBedrockEndpoint(baseURL string) bool {
	value := strings.TrimSpace(baseURL)
	if !strings.Contains(value, "://") {
		return awsRegionPattern.MatchString(value)
	}
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return bedrockHostPattern.MatchString(host)
}
