package providers

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers/workloadidentity"
)

// federatedKeyring returns keys unchanged unless the provider has no API key
// and configures workload identity, in which case it returns a keyring backed
// by a token exchange through the provider's own HTTP client (so a
// configured proxy applies to the exchange too). An API key wins over
// workload identity, matching the official SDKs; the shadowed configuration
// is reported once at startup.
func (f *ProviderFactory) federatedKeyring(cfg ProviderConfig, keys *Keyring, httpClient *http.Client) (*Keyring, error) {
	if !workloadIdentitySet(cfg.WorkloadIdentity, resolvedValueSet) {
		return keys, nil
	}
	if keys.Len() > 0 {
		slog.Warn("provider workload_identity ignored: an API key is also configured and takes precedence",
			"provider", core.LogText(cfg.Name),
			"type", core.LogText(cfg.Type),
			"hint", "remove the API key to authenticate with workload identity")
		return keys, nil
	}
	spec := f.discoveryConfig(cfg.Type)
	if spec.WorkloadIdentity == "" {
		return nil, fmt.Errorf("workload_identity is not supported by provider type %q", cfg.Type)
	}
	endpoint := cfg.WorkloadIdentity.TokenEndpoint
	if endpoint == "" {
		endpoint = workloadidentity.DefaultTokenEndpoint(spec.WorkloadIdentity, ResolveBaseURL(cfg.BaseURL, spec.DefaultBaseURL))
	}
	if httpClient == nil {
		httpClient = httpclient.NewDefaultHTTPClient()
	}
	source, err := workloadidentity.New(workloadidentity.Config{
		Provider:               core.LogText(cfg.Name),
		Grant:                  spec.WorkloadIdentity,
		TokenEndpoint:          endpoint,
		WorkloadIdentityConfig: cfg.WorkloadIdentity,
	}, httpClient)
	if err != nil {
		return nil, err
	}
	return NewTokenSourceKeyring(source), nil
}

// workloadIdentitySet reports whether any workload identity field carries a
// value, as judged by set.
func workloadIdentitySet(w config.WorkloadIdentityConfig, set func(string) bool) bool {
	return slices.ContainsFunc([]string{
		w.IdentityTokenFile, w.IdentityToken, w.TokenEndpoint, w.ServiceAccountID,
		w.IdentityProviderID, w.FederationRuleID, w.OrganizationID, w.WorkspaceID,
	}, set)
}
