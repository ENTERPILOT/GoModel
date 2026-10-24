package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialsService_ReloadReportsSkippedCredentials(t *testing.T) {
	vault := &secretVault{values: map[string]string{"good": "sk-good", "blank": "  "}}
	store := newFakeCredentialStore()
	store.rows["good"] = ManagedProviderCredential{Name: "good", Type: "test", APIKeys: []string{"${vault:good}"}, Enabled: true}
	store.rows["broken"] = ManagedProviderCredential{Name: "broken", Type: "test", APIKeys: []string{"sk-literal", "${vault:gone}"}, Enabled: true}
	store.rows["blank"] = ManagedProviderCredential{Name: "blank", Type: "test", APIKeys: []string{"${vault:blank}"}, Enabled: true}
	store.rows["off"] = ManagedProviderCredential{Name: "off", Type: "test", APIKeys: []string{"${vault:gone}"}}

	svc, _, _ := newSecretsTestService(t, store, vault)

	skipped := svc.Skipped()
	require.Len(t, skipped, 2, "a disabled row is not built, so it is not skipped")
	require.ErrorContains(t, skipped["broken"], "provider_credentials.broken.api_keys[1]")
	assert.NotContains(t, skipped["broken"].Error(), "sk-literal")
	require.ErrorIs(t, skipped["blank"], errCredentialsDidNotResolve, "a key that resolves to blank is skipped too")
	assert.True(t, svc.Installed("good"))
	assert.False(t, svc.Installed("broken"))
}
