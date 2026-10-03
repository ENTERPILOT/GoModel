package litellmmigrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// TestConvertFile_OutputLoadsInGoModel feeds the generated config.yaml to
// GoModel's own strict loader, so a key the gateway does not know fails here
// rather than at an operator's first startup.
func TestConvertFile_OutputLoadsInGoModel(t *testing.T) {
	result, err := ConvertFile(filepath.Join("testdata", "litellm_config.yaml"))
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), result.Config, 0o600))
	t.Chdir(dir)
	t.Setenv("CONFIG_STRICT", "true")
	t.Setenv("AZURE_EU_API_BASE", "") // empty keeps the placeholder unexpanded

	loaded, err := config.Load()
	require.NoError(t, err)
	assert.Contains(t, loaded.RawProviders, "openai")
	assert.Contains(t, loaded.RawProviders, "azure-gpt-4o-eu")
	assert.Equal(t, "${AZURE_EU_API_BASE}/openai/deployments/gpt-4o-eu", loaded.RawProviders["azure-gpt-4o-eu"].BaseURL)
	assert.Len(t, loaded.Config.VirtualModels, 6)
	assert.Equal(t, 2, loaded.Config.Resilience.Retry.MaxRetries)
	assert.Equal(t, 46, loaded.Config.HTTP.Timeout)
	assert.True(t, loaded.Config.Metrics.Enabled)
	assert.Equal(t, config.ConfiguredProviderModelsMode("allowlist"), loaded.Config.Models.ConfiguredProviderModelsMode)
}

func TestConvertFile_Include(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
include: [models.yaml]
model_list:
  - model_name: a
    litellm_params: {model: openai/gpt-4o}
router_settings:
  num_retries: 1
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(`
model_list:
  - model_name: b
    litellm_params: {model: anthropic/claude-sonnet-4-5}
router_settings:
  num_retries: 5
  timeout: 10
`), 0o600))

	result, err := ConvertFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 2, result.Report.Deployments)
	assert.Contains(t, string(result.Config), "max_retries: 1", "the including file wins")
	assert.Contains(t, string(result.Config), "timeout: 10", "included keys fill gaps")
}

func TestConvertFile_IncludeCycle(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("include: [config.yaml]\n"), 0o600))
	_, err := ConvertFile(filepath.Join(dir, "config.yaml"))
	require.ErrorContains(t, err, "include nesting deeper than")
}

func TestEnvFile_RoundTripsThroughDotenv(t *testing.T) {
	values := map[string]string{
		"PLAIN":   "sk-abc123",
		"DOLLAR":  "pa$$word",
		"SPACES":  "a b  c",
		"JSON":    `{"type":"service_account","private_key":"-----BEGIN\nKEY-----"}`,
		"QUOTE":   `it's $HOME`,
		"HASH":    "abc#def",
		"NEWLINE": "line1\nline2",
	}
	var env envFile
	for _, name := range []string{"PLAIN", "DOLLAR", "SPACES", "JSON", "QUOTE", "HASH", "NEWLINE"} {
		env.set(name, values[name])
	}
	parsed, err := godotenv.UnmarshalBytes(env.render())
	require.NoError(t, err)
	assert.Equal(t, values, parsed)
}

func TestEnvFile_SetDisambiguatesConflictingValues(t *testing.T) {
	var env envFile
	assert.Equal(t, "KEY", env.set("KEY", "a"))
	assert.Equal(t, "KEY", env.set("KEY", "a"))
	assert.Equal(t, "KEY_2", env.set("KEY", "b"))
	assert.Nil(t, (&envFile{}).render())
}
