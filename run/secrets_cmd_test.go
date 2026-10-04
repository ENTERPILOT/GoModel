package run

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

func TestParseCLI_SecretsSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"secrets", "reencrypt", "--rotate-data-key"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"reencrypt", "--rotate-data-key"}, opts.SecretsArgs)
	assert.Nil(t, opts.MigrateArgs)
}

func TestParseSecretsArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    *secretsOptions
		wantErr string
	}{
		{name: "reencrypt", args: []string{"reencrypt"}, want: &secretsOptions{}},
		{name: "rotate", args: []string{"reencrypt", "--rotate-data-key"}, want: &secretsOptions{RotateDataKey: true}},
		{name: "help", args: []string{"help"}},
		{name: "reencrypt help", args: []string{"reencrypt", "-h"}},
		{name: "missing action", args: nil, wantErr: "missing action"},
		{name: "unknown action", args: []string{"decrypt"}, wantErr: `unknown action "decrypt"`},
		{name: "extra args", args: []string{"reencrypt", "now"}, wantErr: "unexpected arguments"},
		{name: "unknown flag", args: []string{"reencrypt", "--force"}, wantErr: "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSecretsArgs("gomodel", tt.args, io.Discard, io.Discard)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunSecretsReencrypt(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "secrets.db"))
	t.Setenv("GOMODEL_ENCRYPTION_KEY", "cli-test-key")

	var stdout, stderr bytes.Buffer
	setupConfigCalls := 0
	err := Run(context.Background(), Options{
		Args:   []string{"secrets", "reencrypt"},
		Stdout: &stdout,
		Stderr: &stderr,
		SetupConfig: func(context.Context, *config.LoadResult) error {
			setupConfigCalls++
			return nil
		},
	})
	require.NoError(t, err, stderr.String())
	assert.Equal(t, 1, setupConfigCalls, "the distribution can register a key wrapper for the command")
	assert.Equal(t, "provider_credentials: 0 rows, 0 re-encrypted\n"+
		"mcp_servers: 0 rows, 0 re-encrypted\n"+
		"guardrail_definitions: 0 rows, 0 re-encrypted\n"+
		"active data key: 1\n", stdout.String())
	assert.NotContains(t, stdout.String()+stderr.String(), "cli-test-key")

	t.Setenv("GOMODEL_ENCRYPTION_KEY", "")
	err = Run(context.Background(), Options{Args: []string{"secrets", "reencrypt"}, Stdout: io.Discard, Stderr: &stderr})
	require.ErrorContains(t, err, "GOMODEL_ENCRYPTION_KEY is not set")
	assert.Equal(t, 1, ExitCode(err))

	err = Run(context.Background(), Options{Args: []string{"secrets"}, Stdout: io.Discard, Stderr: io.Discard})
	assert.Equal(t, 2, ExitCode(err))
}

type namedKeyWrapper struct{ id string }

func (w namedKeyWrapper) ID() string { return w.id }
func (namedKeyWrapper) WrapKey(context.Context, []byte) ([]byte, error) {
	return nil, nil
}
func (namedKeyWrapper) UnwrapKey(context.Context, []byte) ([]byte, error) {
	return nil, nil
}

func TestConfigHooksCarryKeyWrapperAcrossGenerations(t *testing.T) {
	first := namedKeyWrapper{id: "kms:first"}
	second := namedKeyWrapper{id: "kms:second"}
	reloadSets := false
	configure := configHooks(t.Context(), Options{
		SetupConfig: func(_ context.Context, result *config.LoadResult) error {
			result.SetKeyWrapper(first, namedKeyWrapper{id: "kms:retired"})
			return nil
		},
		ReloadConfig: func(_ context.Context, result *config.LoadResult) error {
			if reloadSets {
				result.SetKeyWrapper(second)
			}
			return nil
		},
	})
	generation := func() *config.LoadResult {
		result := &config.LoadResult{Config: &config.Config{}}
		require.NoError(t, configure(result))
		return result
	}

	assert.Equal(t, first, generation().KeyWrapper())
	reloaded := generation()
	assert.Equal(t, first, reloaded.KeyWrapper(), "a reload keeps the startup wrapper")
	assert.Equal(t, []config.KeyWrapper{namedKeyWrapper{id: "kms:retired"}}, reloaded.PreviousKeyWrappers())
	reloadSets = true
	assert.Equal(t, second, generation().KeyWrapper(), "ReloadConfig can replace it")
	reloadSets = false
	assert.Equal(t, second, generation().KeyWrapper())
}

func TestRunSecretsReencryptResolvesSecretReferences(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "secrets.db"))
	t.Setenv("REENCRYPT_TEST_KEY", "resolved-key")
	t.Setenv("GOMODEL_ENCRYPTION_KEY", "${env:REENCRYPT_TEST_KEY}")

	var stderr bytes.Buffer
	err := Run(context.Background(), Options{Args: []string{"secrets", "reencrypt"}, Stdout: io.Discard, Stderr: &stderr})
	require.NoError(t, err, stderr.String())

	// The data key was wrapped with the resolved value, not the reference.
	t.Setenv("GOMODEL_ENCRYPTION_KEY", "resolved-key")
	err = Run(context.Background(), Options{Args: []string{"secrets", "reencrypt"}, Stdout: io.Discard, Stderr: &stderr})
	require.NoError(t, err, stderr.String())
}
