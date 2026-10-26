package providers

import (
	"slices"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers/workloadidentity"
)

// workloadIdentityEnvField maps one workload identity setting between its env
// var token (<PROVIDER>[_SUFFIX]_<env>), its config.yaml key, and the grants
// that read it.
type workloadIdentityEnvField struct {
	env    string
	yaml   string
	field  providerEnvField
	grants []workloadidentity.Grant
}

var (
	bothGrants = []workloadidentity.Grant{workloadidentity.TokenExchange, workloadidentity.JWTBearer}

	// workloadIdentityEnvFields follows the variables the provider SDKs
	// document (ANTHROPIC_FEDERATION_RULE_ID, OPENAI_IDENTITY_PROVIDER_ID,
	// ...). A field reads only for the grants that use it, so an unrelated
	// OPENAI_ORGANIZATION_ID never turns workload identity on. Longer tokens
	// come before shorter ones they end with.
	workloadIdentityEnvFields = []workloadIdentityEnvField{
		{"IDENTITY_TOKEN_FILE", "identity_token_file", providerEnvFieldIdentityTokenFile, bothGrants},
		{"IDENTITY_TOKEN", "identity_token", providerEnvFieldIdentityToken, bothGrants},
		{"TOKEN_ENDPOINT", "token_endpoint", providerEnvFieldTokenEndpoint, bothGrants},
		{"SERVICE_ACCOUNT_ID", "service_account_id", providerEnvFieldServiceAccountID, bothGrants},
		{"IDENTITY_PROVIDER_ID", "identity_provider_id", providerEnvFieldIdentityProviderID, []workloadidentity.Grant{workloadidentity.TokenExchange}},
		{"FEDERATION_RULE_ID", "federation_rule_id", providerEnvFieldFederationRuleID, []workloadidentity.Grant{workloadidentity.JWTBearer}},
		{"ORGANIZATION_ID", "organization_id", providerEnvFieldOrganizationID, []workloadidentity.Grant{workloadidentity.JWTBearer}},
		{"WORKSPACE_ID", "workspace_id", providerEnvFieldWorkspaceID, []workloadidentity.Grant{workloadidentity.JWTBearer}},
	}
)

// workloadIdentitySetting returns the setting field selects in w, or nil
// when field is not a workload identity field.
func workloadIdentitySetting(w *config.WorkloadIdentityConfig, field providerEnvField) *string {
	switch field {
	case providerEnvFieldIdentityTokenFile:
		return &w.IdentityTokenFile
	case providerEnvFieldIdentityToken:
		return &w.IdentityToken
	case providerEnvFieldTokenEndpoint:
		return &w.TokenEndpoint
	case providerEnvFieldServiceAccountID:
		return &w.ServiceAccountID
	case providerEnvFieldIdentityProviderID:
		return &w.IdentityProviderID
	case providerEnvFieldFederationRuleID:
		return &w.FederationRuleID
	case providerEnvFieldOrganizationID:
		return &w.OrganizationID
	case providerEnvFieldWorkspaceID:
		return &w.WorkspaceID
	default:
		return nil
	}
}

// providerEnvToken pairs an env var token with the field it sets, in the
// shape parseProviderEnvKey matches against.
type providerEnvToken = struct {
	name  string
	field providerEnvField
}

// workloadIdentityEnvNames returns the env var tokens a provider type with
// grant accepts, in match order; none when it supports no workload identity.
func workloadIdentityEnvNames(grant workloadidentity.Grant) []providerEnvToken {
	if grant == "" {
		return nil
	}
	var names []providerEnvToken
	for _, f := range workloadIdentityEnvFields {
		if slices.Contains(f.grants, grant) {
			names = append(names, providerEnvToken{f.env, f.field})
		}
	}
	return names
}

// overlayWorkloadIdentity copies every env-set workload identity field onto
// existing.
func overlayWorkloadIdentity(existing *config.WorkloadIdentityConfig, env config.WorkloadIdentityConfig) {
	for _, f := range workloadIdentityEnvFields {
		if value := *workloadIdentitySetting(&env, f.field); value != "" {
			*workloadIdentitySetting(existing, f.field) = value
		}
	}
}
