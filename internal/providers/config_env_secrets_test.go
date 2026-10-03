package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProviderEnvSecrets(t *testing.T) {
	t.Setenv("GOMODEL_TEST_OPENAI_KEY", "sk-from-env")
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		if reference == "prod/anthropic" {
			return "sk-from-vault", nil
		}
		return "", errors.New("not found")
	})))

	tests := []struct {
		name      string
		environ   []string
		want      []string
		wantField string
		wantErr   string
	}{
		{
			name:    "references in provider variables resolve",
			environ: []string{"OPENAI_API_KEY=${env:GOMODEL_TEST_OPENAI_KEY}", "ANTHROPIC_API_KEY_2=${vault:prod/anthropic}", "OPENAI_EU_PROXY_URL=http://u:${vault:prod/anthropic}@p"},
			want:    []string{"OPENAI_API_KEY=sk-from-env", "ANTHROPIC_API_KEY_2=sk-from-vault", "OPENAI_EU_PROXY_URL=http://u:sk-from-vault@p"},
		},
		{
			name:    "legacy placeholders keep their historical treatment",
			environ: []string{"OPENAI_API_KEY=${UNSET_VAR}", "OPENAI_BASE_URL=plain"},
			want:    []string{"OPENAI_API_KEY=${UNSET_VAR}", "OPENAI_BASE_URL=plain"},
		},
		{
			name:    "other variables are not provider configuration",
			environ: []string{"UNRELATED=${vault:missing}", "OPENAI_SOMETHING_ELSE=${vault:missing}"},
			want:    []string{"UNRELATED=${vault:missing}", "OPENAI_SOMETHING_ELSE=${vault:missing}"},
		},
		{
			name:      "unresolved reference is an error naming the variable",
			environ:   []string{"OPENAI_API_KEY=${vault:prod/missing}"},
			wantField: "OPENAI_API_KEY",
			wantErr:   "OPENAI_API_KEY: secret reference ${vault:...}: not found",
		},
		{
			name:      "unknown scheme",
			environ:   []string{"GEMINI_API_KEY=${aws:key}"},
			wantField: "GEMINI_API_KEY",
			wantErr:   "unknown secret scheme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveProviderEnvSecrets(t.Context(), secrets, tt.environ, testDiscoveryConfigs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				secretErr, ok := errors.AsType[*config.SecretError](err)
				require.True(t, ok)
				assert.Equal(t, tt.wantField, secretErr.Field)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveProviderEnvSecretsDoesNotModifyInput(t *testing.T) {
	t.Setenv("GOMODEL_TEST_OPENAI_KEY", "sk-from-env")
	environ := []string{"OPENAI_API_KEY=${env:GOMODEL_TEST_OPENAI_KEY}"}
	got, err := resolveProviderEnvSecrets(t.Context(), nil, environ, testDiscoveryConfigs)
	require.NoError(t, err)
	assert.Equal(t, []string{"OPENAI_API_KEY=sk-from-env"}, got)
	assert.Equal(t, []string{"OPENAI_API_KEY=${env:GOMODEL_TEST_OPENAI_KEY}"}, environ)
}

func TestInit_ResolvesProviderEnvSecretReferences(t *testing.T) {
	newFactory := func(got *ProviderConfig) *ProviderFactory {
		factory := NewProviderFactory()
		factory.Add(Registration{
			Type: "test",
			New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
				*got = cfg
				return &initTestProvider{}
			},
		})
		return factory
	}
	loadResult := func() *config.LoadResult {
		return &config.LoadResult{
			Config: &config.Config{Cache: config.CacheConfig{Model: config.ModelCacheConfig{
				RefreshInterval: 1,
				Local:           &config.LocalCacheConfig{CacheDir: t.TempDir()},
			}}},
			RawProviders: map[string]config.RawProviderConfig{},
			Secrets:      config.NewSecrets(),
		}
	}

	t.Run("resolved", func(t *testing.T) {
		t.Setenv("GOMODEL_TEST_KEY", "sk-resolved")
		t.Setenv("TEST_API_KEY", "${env:GOMODEL_TEST_KEY}")
		t.Setenv("TEST_BASE_URL", "http://localhost:1")
		var got ProviderConfig
		result, err := Init(t.Context(), loadResult(), newFactory(&got))
		require.NoError(t, err)
		t.Cleanup(func() { _ = result.Close() })
		assert.Equal(t, []string{"sk-resolved"}, got.APIKeys)
	})

	t.Run("unresolved stops startup", func(t *testing.T) {
		t.Setenv("TEST_API_KEY", "${vault:prod/test}")
		var got ProviderConfig
		_, err := Init(t.Context(), loadResult(), newFactory(&got))
		require.ErrorContains(t, err, "TEST_API_KEY: secret reference ${vault:...}")
		assert.Empty(t, got.APIKeys, "a provider must not be built from an unresolved reference")
	})
}

func TestResolveProviderEnvSecretsPassesTheVariableAsField(t *testing.T) {
	var field string
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(ctx context.Context, _ string) (string, error) {
		field, _ = config.SecretFieldFromContext(ctx)
		return "sk", nil
	})))
	_, err := resolveProviderEnvSecrets(t.Context(), secrets, []string{"OPENAI_API_KEY_2=${vault:x}"}, testDiscoveryConfigs)
	require.NoError(t, err)
	assert.Equal(t, "OPENAI_API_KEY_2", field)
}
