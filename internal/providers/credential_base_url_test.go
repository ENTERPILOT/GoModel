package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCredential_BaseURL(t *testing.T) {
	schema := credentialSchema("keyless", DiscoveryConfig{AllowAPIKeyless: true})

	tests := []struct {
		name    string
		baseURL string
		valid   bool
	}{
		{name: "unset", baseURL: "", valid: true},
		{name: "https origin", baseURL: "https://api.openai.com/v1", valid: true},
		{name: "upper-case scheme", baseURL: "HTTPS://api.openai.com/v1", valid: true},
		{name: "trailing slash", baseURL: "https://api.example.com/", valid: true},
		{name: "loopback name", baseURL: "http://localhost:11434/v1", valid: true},
		{name: "loopback IPv4", baseURL: "http://127.0.0.1:8000/v1", valid: true},
		{name: "loopback IPv6", baseURL: "http://[::1]:1234/v1", valid: true},
		{name: "private 10/8", baseURL: "http://10.0.0.5:8000/v1", valid: true},
		{name: "private 172.16/12", baseURL: "http://172.17.0.1:11434", valid: true},
		{name: "private 192.168/16", baseURL: "http://192.168.1.20:1234/v1", valid: true},
		{name: "unique local IPv6", baseURL: "http://[fd12:3456:789a::1]:8000/v1", valid: true},
		{name: "single-label service name", baseURL: "http://ollama:11434/v1", valid: true},
		{name: "cluster service name", baseURL: "http://vllm.models.svc.cluster.local:8000/v1", valid: true},
		{name: "host label that starts with digits", baseURL: "https://123.example.com/v1", valid: true},
		{name: "AWS region", baseURL: "us-east-1", valid: true},
		{name: "AWS GovCloud region", baseURL: "us-gov-west-1", valid: true},

		{name: "no scheme", baseURL: "localhost:11434/v1"},
		{name: "scheme-relative", baseURL: "//api.example.com/v1"},
		{name: "other scheme", baseURL: "ftp://api.example.com/v1"},
		{name: "file scheme", baseURL: "file:///etc/hosts"},
		{name: "no host", baseURL: "https:///v1"},
		{name: "malformed", baseURL: "https://api.example.com:port/v1"},
		{name: "userinfo", baseURL: "https://user:s3cret@api.example.com/v1"},
		{name: "username only", baseURL: "https://user@api.example.com/v1"},
		{name: "query", baseURL: "https://api.example.com/v1?x=1"},
		{name: "empty query", baseURL: "https://api.example.com/v1?"},
		{name: "fragment", baseURL: "https://api.example.com/v1#x"},
		{name: "empty fragment", baseURL: "https://api.example.com/v1#"},
		{name: "IPv4 link-local", baseURL: "http://169.254.169.254/latest"},
		{name: "IPv4 link-local, other host", baseURL: "http://169.254.170.2/v1"},
		{name: "IPv4 link-local, trailing dot", baseURL: "http://169.254.169.254./v1"},
		{name: "IPv4-mapped link-local", baseURL: "http://[::ffff:169.254.169.254]/v1"},
		{name: "IPv6 link-local", baseURL: "http://[fe80::1]/v1"},
		{name: "IPv6 link-local with zone", baseURL: "http://[fe80::1%25eth0]/v1"},
		{name: "IPv6 metadata address", baseURL: "http://[fd00:ec2::254]/v1"},
		{name: "fixed metadata address", baseURL: "http://100.100.100.200/v1"},
		{name: "decimal IPv4", baseURL: "http://2852039166/v1"},
		{name: "hex IPv4", baseURL: "http://0xa9fea9fe/v1"},
		{name: "short IPv4", baseURL: "http://169.254.43518/v1"},
		{name: "metadata host", baseURL: "http://metadata.google.internal/v1"},
		{name: "metadata host, mixed case and trailing dot", baseURL: "http://Metadata.Google.Internal./v1"},
		{name: "metadata short name", baseURL: "http://metadata/v1"},
		{name: "instance-data host", baseURL: "http://instance-data/latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredential(ManagedProviderCredential{Name: "p", Type: "keyless", BaseURL: tt.baseURL}, schema)
			if tt.valid {
				require.NoError(t, err)

				return
			}
			assertCredentialField(t, err, CredentialFieldBaseURL)
			assert.NotContains(t, err.Error(), "s3cret", "the rejection must not echo the value")
		})
	}
}

// A rejected base URL is refused before the row is stored or its provider is
// built, so nothing is sent to it.
func TestCredentialsService_UpsertRejectsAnInvalidBaseURL(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)

	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name: "p", Type: "test", APIKeys: []string{"sk-test"}, BaseURL: "https://api.example.com/v1?x=", Enabled: true,
	})
	assertCredentialField(t, err, CredentialFieldBaseURL)
	assert.Equal(t, 0, registry.ProviderCount())
	rows, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}
