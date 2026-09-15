package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestPassthroughExecutionTarget_PrefersWorkflow(t *testing.T) {
	c, _ := echotest.Post(t, "/p/openai/v1/responses?trace=1", nil)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "v1/responses",
			NormalizedEndpoint: "responses",
			AuditPath:          "/v1/responses",
		},
	})))

	providerType, _, endpoint, info, err := passthroughExecutionTarget(c, nil, false)
	require.NoError(t, err)
	require.Equal(t, "openai", providerType)
	require.Equal(t, "responses?trace=1", endpoint)
	require.NotNil(t, info)
	require.Equal(t, "responses", info.NormalizedEndpoint)
}

func TestPassthroughExecutionTarget_NormalizesFallbackFromPath(t *testing.T) {
	c, _ := echotest.Post(t, "/p/openai/v1/responses?trace=1", nil)

	providerType, _, endpoint, info, err := passthroughExecutionTarget(c, nil, true)
	require.NoError(t, err)
	require.Equal(t, "openai", providerType)
	require.Equal(t, "responses?trace=1", endpoint)
	require.NotNil(t, info)
	require.Equal(t, "responses", info.NormalizedEndpoint)
}

func TestPassthroughExecutionTarget_ResolvesConfiguredProviderNameToType(t *testing.T) {
	c, _ := echotest.Post(t, "/p/openai_test/v1/responses?trace=1", nil)

	provider := &mockProvider{
		providerTypes: map[string]string{
			"openai_test/gpt-5-mini": "openai",
		},
		providerNames: map[string]string{
			"openai_test/gpt-5-mini": "openai_test",
		},
	}

	providerType, providerName, endpoint, info, err := passthroughExecutionTarget(c, provider, true)
	require.NoError(t, err)
	require.Equal(t, "openai", providerType)
	require.Equal(t, "openai_test", providerName)
	require.Equal(t, "responses?trace=1", endpoint)
	require.NotNil(t, info)
	require.Equal(t, "openai", info.Provider)
	require.Equal(t, "openai_test", info.ProviderName)
}
