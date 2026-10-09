package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialsService_ReloadReportsCredentialsWhoseReferencesFail(t *testing.T) {
	vault := &secretVault{values: map[string]string{"good": "sk-good"}}
	store := newFakeCredentialStore()
	store.rows["good"] = ManagedProviderCredential{Name: "good", Type: "test", APIKeys: []string{"${vault:good}"}, Enabled: true}
	store.rows["broken"] = ManagedProviderCredential{Name: "broken", Type: "test", APIKeys: []string{"sk-literal", "${vault:gone}"}, Enabled: true}
	store.rows["unknown"] = ManagedProviderCredential{Name: "unknown", Type: "no-such-type", APIKeys: []string{"sk-literal"}, Enabled: true}

	svc, _, _ := newSecretsTestService(t, store, vault)

	unresolved := svc.UnresolvedSecrets()
	require.Len(t, unresolved, 1, "only reference failures are reported")
	require.Contains(t, unresolved, "broken")
	require.ErrorContains(t, unresolved["broken"], "provider_credentials.broken.api_keys[1]")
	assert.NotContains(t, unresolved["broken"].Error(), "sk-literal")
	assert.True(t, svc.Installed("good"))
	assert.False(t, svc.Installed("broken"))
}
