package awscfg

import (
	"net/url"
	"regexp"
	"strings"
)

// regionExpr is the shape of an AWS region name: "us-east-1",
// "us-gov-west-1", "eusc-de-east-1".
const regionExpr = `[a-z]{2,}(-[a-z]+)+-[0-9]+`

var regionPattern = regexp.MustCompile(`^` + regionExpr + `$`)

// bedrockHostPattern matches the hosts AWS serves Bedrock on: a Bedrock
// service name, a region, and an AWS DNS suffix, optionally behind a VPC
// interface endpoint. It is deliberately narrower than "any AWS host": other
// names under the same suffixes (API gateways, load balancers, buckets,
// instances) belong to AWS customers.
var bedrockHostPattern = regexp.MustCompile(
	`^(vpce-[a-z0-9-]+\.)?bedrock(-[a-z]+)*\.` + regionExpr + `\.(vpce\.)?(amazonaws\.com|amazonaws\.com\.cn|amazonaws\.eu|api\.aws)$`)

// IsRegion reports whether s has the shape of an AWS region name, so the SDK
// forms an AWS host from it rather than one a dotted value could name.
func IsRegion(s string) bool {
	return regionPattern.MatchString(s)
}

// IsBedrockEndpoint reports whether rawURL is an https URL on one of AWS's
// own Bedrock hosts and the default port: regional, FIPS, dual-stack, and VPC
// endpoints included. Adapters use it to decide whether an endpoint may
// receive a credential taken from the gateway's host rather than from the
// provider's own configuration.
func IsBedrockEndpoint(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return bedrockHostPattern.MatchString(host)
}
