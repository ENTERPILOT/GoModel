package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const migrateTestConfig = `
model_list:
  - model_name: gpt-4o
    litellm_params:
      model: openai/gpt-4o
      api_key: sk-inline-secret
general_settings:
  master_key: sk-1234
`

func writeLiteLLMConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "litellm.yaml")
	require.NoError(t, os.WriteFile(path, []byte(migrateTestConfig), 0o600))
	return path
}

func TestParseCLI_MigrateSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"migrate", "litellm", "x.yaml"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"litellm", "x.yaml"}, opts.MigrateArgs)
	assert.Nil(t, opts.PluginArgs)
}

func TestRunMigrateCommand_Usage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "missing source", args: nil, wantCode: 2, wantErr: "missing source"},
		{name: "unknown source", args: []string{"portkey"}, wantCode: 2, wantErr: `unknown source "portkey"`},
		{name: "missing file", args: []string{"litellm"}, wantCode: 2, wantErr: "missing LiteLLM config file argument"},
		{name: "extra args", args: []string{"litellm", "a.yaml", "b.yaml"}, wantCode: 2, wantErr: "unexpected arguments"},
		{name: "help", args: []string{"help"}, wantCode: 0},
		{name: "litellm help", args: []string{"litellm", "-h"}, wantCode: 0},
		{name: "file not found", args: []string{"litellm", filepath.Join(t.TempDir(), "nope.yaml")}, wantCode: 1, wantErr: "nope.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runMigrateCommand("gomodel", tt.args, &stdout, &stderr)
			assert.Equal(t, tt.wantCode, ExitCode(err))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Contains(t, stderr.String(), tt.wantErr, "errors are printed, not only returned")
			}
		})
	}
}

func TestRunMigrateCommand_DryRunWritesNothing(t *testing.T) {
	path := writeLiteLLMConfig(t)
	var stdout bytes.Buffer
	require.NoError(t, runMigrateCommand("gomodel", []string{"litellm", path}, &stdout, io.Discard))

	out := stdout.String()
	assert.Contains(t, out, "# LiteLLM to GoModel migration report")
	assert.Contains(t, out, "virtual_models:")
	assert.Contains(t, out, "Dry run: nothing was written.")
	assert.NotContains(t, out, "sk-inline-secret", "the dry run never prints secrets")
	assert.NotContains(t, out, "sk-1234")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestRunMigrateCommand_WritesFiles(t *testing.T) {
	path := writeLiteLLMConfig(t)
	out := filepath.Join(t.TempDir(), "gomodel")
	args := []string{"litellm", "--out", out, path}
	require.NoError(t, runMigrateCommand("gomodel", args, io.Discard, io.Discard))

	config, err := os.ReadFile(filepath.Join(out, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(config), "api_key: ${OPENAI_API_KEY}")
	assert.NotContains(t, string(config), "sk-inline-secret")

	env, err := os.ReadFile(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "OPENAI_API_KEY=sk-inline-secret")
	assert.Contains(t, string(env), "GOMODEL_MASTER_KEY=sk-1234")
	info, err := os.Stat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.FileExists(t, filepath.Join(out, "MIGRATION_REPORT.md"))

	err = runMigrateCommand("gomodel", args, io.Discard, io.Discard)
	require.ErrorContains(t, err, "already exists; pass --force to overwrite")

	require.NoError(t, os.Chmod(filepath.Join(out, ".env"), 0o644))
	require.NoError(t, runMigrateCommand("gomodel", append([]string{"litellm", "--force"}, args[1:]...), io.Discard, io.Discard))
	info, err = os.Stat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "--force restores the secret file's mode")
}

func TestRunMigrateCommand_DoesNotFollowSymlinks(t *testing.T) {
	path := writeLiteLLMConfig(t)
	out := t.TempDir()
	outside := filepath.Join(t.TempDir(), "stolen.env")
	require.NoError(t, os.Symlink(outside, filepath.Join(out, ".env")))
	args := []string{"litellm", "--out", out, path}

	err := runMigrateCommand("gomodel", args, io.Discard, io.Discard)
	require.ErrorContains(t, err, "already exists", "a dangling symlink counts as an existing file")

	require.NoError(t, runMigrateCommand("gomodel", append([]string{"litellm", "--force"}, args[1:]...), io.Discard, io.Discard))
	assert.NoFileExists(t, outside, "the secret file is not written through the link")
	info, err := os.Lstat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
