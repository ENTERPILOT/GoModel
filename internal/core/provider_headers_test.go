package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEdgeCredentialHeadersAreCredentialHeaders(t *testing.T) {
	for name := range edgeCredentialHeaders {
		assert.True(t, IsCredentialHeader(name), "%s must stay redacted as a credential", name)
		assert.Equal(t, canonicalHeaderName(name), name, "edgeCredentialHeaders keys must be canonical")
	}
}

func TestIsProviderCredentialHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "Authorization", want: true},
		{name: "authorization", want: true},
		{name: " X-Api-Key ", want: true},
		{name: "X_Api_Key", want: true},
		{name: "api-key", want: true},
		{name: "x-goog-api-key", want: true},
		{name: "xi-api-key", want: true},
		{name: "Cookie", want: true},
		{name: "Proxy-Authorization", want: true},
		// Credentials for a proxy in front of the provider are what the
		// setting is for.
		{name: "cf-aig-authorization", want: false},
		{name: "Ocp-Apim-Subscription-Key", want: false},
		{name: "ocp_apim_subscription_key", want: false},
		{name: "Helicone-Auth", want: false},
		{name: "x-portkey-api-key", want: false},
		{name: "X-Edge-Auth", want: false},
		{name: "OpenAI-Organization", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsProviderCredentialHeader(tt.name))
		})
	}
}

func TestIsProviderHeaderReserved(t *testing.T) {
	for name := range pluginUpstreamTransportHeaders {
		assert.True(t, IsProviderHeaderReserved(name), name)
	}
	tests := []struct {
		name string
		want bool
	}{
		{name: "host", want: true},
		{name: "Content_Type", want: true},
		{name: "accept-encoding", want: true},
		{name: "Idempotency-Key", want: true},
		{name: "idempotency_key", want: true},
		{name: "Proxy-Authorization", want: true},
		{name: "proxy-anything", want: true},
		{name: "X-Proxy-Token", want: false},
		{name: "X-Tenant", want: false},
		{name: "OpenAI-Project", want: false},
		// Owned per provider type, not for every provider.
		{name: "anthropic-beta", want: false},
		{name: "User-Agent", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsProviderHeaderReserved(tt.name))
		})
	}
}
