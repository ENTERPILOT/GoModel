// Package workloadidentity exchanges a workload's platform identity token
// (Kubernetes, AWS, GCP, Azure, GitHub Actions, SPIFFE) for a short-lived
// provider access token, so a provider can authenticate without a static API
// key.
//
// A Source caches the access token and refreshes it ahead of expiry. The
// identity token file is re-read on every exchange because projected tokens
// rotate on disk. Neither the identity token nor the access token is ever
// logged or included in an error.
package workloadidentity

import (
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// Grant selects the OAuth grant a provider's token endpoint speaks.
type Grant string

const (
	// TokenExchange is the RFC 8693 token exchange OpenAI uses.
	TokenExchange Grant = "token_exchange"
	// JWTBearer is the RFC 7523 jwt-bearer grant Anthropic uses.
	JWTBearer Grant = "jwt_bearer"
)

// OpenAITokenEndpoint is the default endpoint for the TokenExchange grant.
const OpenAITokenEndpoint = "https://auth.openai.com/oauth/token" //nolint:gosec // G101: a public endpoint URL, not a credential.

// Config is a resolved workload identity configuration for one provider.
type Config struct {
	// Provider names the provider instance in logs.
	Provider string
	Grant    Grant
	// TokenEndpoint is the exchange URL; see DefaultTokenEndpoint.
	TokenEndpoint string
	config.WorkloadIdentityConfig
}

// DefaultTokenEndpoint returns the exchange endpoint used when none is
// configured. The jwt-bearer endpoint lives under the provider's API base
// URL (Anthropic: <base_url>/oauth/token), so a custom base URL moves it too.
func DefaultTokenEndpoint(grant Grant, baseURL string) string {
	switch grant {
	case TokenExchange:
		return OpenAITokenEndpoint
	case JWTBearer:
		return strings.TrimRight(baseURL, "/") + "/oauth/token"
	default:
		return ""
	}
}

// Validate reports the first missing or unsupported setting. Errors name
// fields only, never values.
func (c Config) Validate() error {
	if strings.TrimSpace(c.TokenEndpoint) == "" {
		return errors.New("workload_identity: token_endpoint is required")
	}
	if blank(c.IdentityTokenFile) && blank(c.IdentityToken) {
		return errors.New("workload_identity: identity_token_file or identity_token is required")
	}
	required := map[string]string{"service_account_id": c.ServiceAccountID}
	switch c.Grant {
	case TokenExchange:
		required["identity_provider_id"] = c.IdentityProviderID
	case JWTBearer:
		required["federation_rule_id"] = c.FederationRuleID
		required["organization_id"] = c.OrganizationID
	default:
		return fmt.Errorf("workload_identity: unsupported grant %q", c.Grant)
	}
	for _, field := range []string{"identity_provider_id", "federation_rule_id", "organization_id", "service_account_id"} {
		if value, ok := required[field]; ok && blank(value) {
			return fmt.Errorf("workload_identity: %s is required", field)
		}
	}
	return nil
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}
