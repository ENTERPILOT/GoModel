package admin

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestProviderCredentialView_ShowsReferencesAndMasksLiterals(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["openai"] = providers.ManagedProviderCredential{
		Name:                     "openai",
		Type:                     "openai",
		APIKeys:                  []string{"${vault:llm#openai}", "sk-literal"},
		ServiceAccountJSON:       "${file:/run/secrets/sa.json}",
		ServiceAccountJSONBase64: "eyJsaXRlcmFsIjp0cnVlfQ==",
		ProxyURL:                 "http://gomodel:${file:/run/secrets/proxy}@proxy:3128",
		Enabled:                  true,
	}
	fake.rows["literal-proxy"] = providers.ManagedProviderCredential{
		Name:     "literal-proxy",
		Type:     "openai",
		APIKeys:  []string{"sk-other"},
		ProxyURL: "http://gomodel:hunter2@proxy:3128",
		Enabled:  true,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Get(t, "/admin/provider-credentials")
	require.NoError(t, h.ListProviderCredentials(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, secret := range []string{"sk-literal", "eyJsaXRlcmFsIjp0cnVlfQ==", "hunter2", "sk-other"} {
		assert.NotContains(t, rec.Body.String(), secret)
	}

	byName := map[string]providerCredentialViewResponse{}
	for _, view := range echotest.Decode[[]providerCredentialViewResponse](t, rec) {
		byName[view.Name] = view
	}
	view := byName["openai"]
	assert.Equal(t, []string{"${vault:llm#openai}", redactedCredentialValue}, view.APIKeys)
	assert.Equal(t, "${file:/run/secrets/sa.json}", view.ServiceAccountJSON)
	assert.Equal(t, redactedCredentialValue, view.ServiceAccountJSONBase64)
	assert.Equal(t, "http://gomodel:${file:/run/secrets/proxy}@proxy:3128", view.ProxyURL)
	assert.NotContains(t, byName["literal-proxy"].ProxyURL, "hunter2")
}

func TestUpsertProviderCredential_RoundTripsReferencesAndMasks(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["openai"] = providers.ManagedProviderCredential{
		Name:     "openai",
		Type:     "openai",
		APIKeys:  []string{"${vault:old}", "sk-stored"},
		ProxyURL: "http://u:${env:PROXY_PASS}@proxy:3128",
		Enabled:  true,
	}
	h := newProviderCredentialsHandler(fake)

	// The client sends back what the view showed: the reference as is, the
	// mask for the literal; then swaps the first reference for a new one.
	body := `{"name":"openai","type":"openai","api_keys":["${vault:new}","***********","${env:THIRD}"],"proxy_url":"http://u:${env:PROXY_PASS}@proxy:3128"}`
	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", body)
	require.NoError(t, h.UpsertProviderCredential(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored := fake.rows["openai"]
	assert.Equal(t, []string{"${vault:new}", "sk-stored", "${env:THIRD}"}, stored.APIKeys)
	assert.Equal(t, "http://u:${env:PROXY_PASS}@proxy:3128", stored.ProxyURL)

	view := echotest.Decode[providerCredentialViewResponse](t, rec)
	assert.Equal(t, []string{"${vault:new}", redactedCredentialValue, "${env:THIRD}"}, view.APIKeys)
}

func TestUpsertProviderCredential_UnresolvableReferenceIs400(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.upsertErr = &providers.CredentialFieldError{
		Field:   providers.CredentialFieldAPIKeys,
		Message: "provider_credentials.openai.api_keys[0]: secret reference ${vault:...}: unknown secret scheme",
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"openai","type":"openai","api_keys":["${vault:x}"]}`)
	require.NoError(t, h.UpsertProviderCredential(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"param":"api_keys"`)
}

func TestMCPServerView_ShowsHeaderReferences(t *testing.T) {
	fake := newMCPAdminFake()
	server := mcpgateway.ManagedServer{
		Name:      "github",
		URL:       "https://mcp.example.com/mcp",
		Transport: "http",
		Headers:   map[string]string{"Authorization": "Bearer ${env:GITHUB_TOKEN}", "X-Api-Key": "literal-key"},
		Enabled:   true,
	}
	fake.addStored(server, mcpgateway.StatusConnected)
	view := fake.views["github"]
	// What the gateway runs with: resolved headers, references kept aside.
	view.Spec.Headers = map[string]string{"Authorization": "Bearer ghp_resolved", "X-Api-Key": "literal-key"}
	view.Spec.HeaderReferences = map[string]string{"Authorization": "Bearer ${env:GITHUB_TOKEN}"}
	fake.views["github"] = view
	h := newMCPHandler(fake)

	c, rec := echotest.Get(t, "/admin/mcp-servers")
	require.NoError(t, h.ListMCPServers(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "ghp_resolved")
	assert.NotContains(t, rec.Body.String(), "literal-key")

	views := echotest.Decode[[]mcpServerViewResponse](t, rec)
	require.Len(t, views, 1)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${env:GITHUB_TOKEN}", "X-Api-Key": redactedMCPHeaderValue}, views[0].Headers)
}

func TestUpsertMCPServer_StoresReferenceAndKeepsMaskedValue(t *testing.T) {
	fake := newMCPAdminFake()
	fake.addStored(mcpgateway.ManagedServer{
		Name:      "github",
		URL:       "https://mcp.example.com/mcp",
		Transport: "http",
		Headers:   map[string]string{"Authorization": "Bearer ${vault:old}", "X-Api-Key": "literal-key"},
		Enabled:   true,
	}, mcpgateway.StatusConnected)
	h := newMCPHandler(fake)

	body := `{"name":"github","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer ${vault:new}","X-Api-Key":"***"}}`
	c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", body)
	require.NoError(t, h.UpsertMCPServer(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored := fake.stored["github"]
	assert.Equal(t, "Bearer ${vault:new}", stored.Headers["Authorization"])
	assert.Equal(t, "literal-key", stored.Headers["X-Api-Key"])
}

func TestUpsertMCPServer_UnresolvableReferenceIs400(t *testing.T) {
	fake := newMCPAdminFake()
	fake.upsertErr = &config.SecretError{Field: "mcp_servers.github.headers.Authorization", Scheme: "vault", Err: config.ErrUnknownSecretScheme}
	h := newMCPHandler(fake)

	body := `{"name":"github","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer ${vault:x}"}}`
	c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", body)
	require.NoError(t, h.UpsertMCPServer(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "mcp_servers.github.headers.Authorization")

	gatewayErr, ok := errors.AsType[*core.GatewayError](mcpServerWriteError(errors.New("store down")))
	require.True(t, ok)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode(), "other failures stay 502")
}

func TestSecretRedaction_MasksValuesMixingLiteralsAndReferences(t *testing.T) {
	assert.Equal(t, "${vault:a}${env:B}", redactCredentialValue("${vault:a}${env:B}"))
	assert.Equal(t, redactedCredentialValue, redactCredentialValue("sk-live-${env:TAIL}"))
	assert.Equal(t, redactedCredentialValue, redactCredentialValue("${env:HEAD}-sk-live"))

	assert.True(t, mcpHeaderShown("Bearer ${env:TOKEN}"))
	assert.True(t, mcpHeaderShown("${env:TOKEN}"))
	assert.False(t, mcpHeaderShown("Bearer sk-literal-${env:SUFFIX}"))
	assert.False(t, mcpHeaderShown("sk-literal ${env:SUFFIX}"))

	assert.Equal(t, "http://u:${file:/run/secrets/p}@proxy:3128", redactProxyURL("http://u:${file:/run/secrets/p}@proxy:3128"))
	assert.Equal(t, "http://${env:PROXY_HOST}:3128", redactProxyURL("http://${env:PROXY_HOST}:3128"))
	assert.Equal(t, redactedCredentialValue, redactProxyURL("http://u:hunter2@${env:PROXY_HOST}:3128"))
	assert.Equal(t, redactedCredentialValue, redactProxyURL("http://u:pre${env:P}@proxy:3128"))
}

func TestUpsertProviderCredential_MixedValueRoundTripsAsMask(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["openai"] = providers.ManagedProviderCredential{
		Name:     "openai",
		Type:     "openai",
		APIKeys:  []string{"sk-live-${env:TAIL}"},
		ProxyURL: "http://u:hunter2@${env:PROXY_HOST}:3128",
		Enabled:  true,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Get(t, "/admin/provider-credentials")
	require.NoError(t, h.ListProviderCredentials(c))
	assert.NotContains(t, rec.Body.String(), "sk-live")
	assert.NotContains(t, rec.Body.String(), "hunter2")

	body := `{"name":"openai","type":"openai","api_keys":["***********"],"proxy_url":"***********"}`
	c, rec = echotest.Request(t, http.MethodPut, "/admin/provider-credentials", body)
	require.NoError(t, h.UpsertProviderCredential(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"sk-live-${env:TAIL}"}, fake.rows["openai"].APIKeys)
	assert.Equal(t, "http://u:hunter2@${env:PROXY_HOST}:3128", fake.rows["openai"].ProxyURL)
}

func TestMCPServerView_MasksHeaderMixingLiteralAndReference(t *testing.T) {
	fake := newMCPAdminFake()
	fake.addStored(mcpgateway.ManagedServer{Name: "github", URL: "https://mcp.example.com/mcp", Transport: "http", Enabled: true}, mcpgateway.StatusConnected)
	view := fake.views["github"]
	view.Spec.Headers = map[string]string{"Authorization": "Bearer sk-literal-tail"}
	view.Spec.HeaderReferences = map[string]string{"Authorization": "Bearer sk-literal-${env:SUFFIX}"}
	fake.views["github"] = view
	h := newMCPHandler(fake)

	c, rec := echotest.Get(t, "/admin/mcp-servers")
	require.NoError(t, h.ListMCPServers(c))
	assert.NotContains(t, rec.Body.String(), "sk-literal")
	views := echotest.Decode[[]mcpServerViewResponse](t, rec)
	require.Len(t, views, 1)
	assert.Equal(t, redactedMCPHeaderValue, views[0].Headers["Authorization"])
}
