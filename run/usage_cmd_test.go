package run

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestParseCLI_UsageSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"usage", "import", "other.db"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"import", "other.db"}, opts.UsageArgs)
	assert.Nil(t, opts.SecretsArgs)
}

func TestParseUsageArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "import", args: []string{"import", "other.db"}, want: "other.db"},
		{name: "help", args: []string{"help"}},
		{name: "import help", args: []string{"import", "--help"}},
		{name: "missing action", args: nil, wantErr: "missing action"},
		{name: "unknown action", args: []string{"export"}, wantErr: `unknown action "export"`},
		{name: "missing source", args: []string{"import"}, wantErr: "missing source"},
		{name: "extra args", args: []string{"import", "a.db", "b.db"}, wantErr: "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUsageArgs("gomodel", tt.args, io.Discard, io.Discard)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunUsageImport(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()

	sourcePath := filepath.Join(t.TempDir(), "other.db")
	src, err := storage.NewSQLite(storage.SQLiteConfig{Path: sourcePath})
	require.NoError(t, err)
	srcDB, err := sqlx.NewSQLite(src.DB())
	require.NoError(t, err)
	srcStore, err := usage.NewSQLStore(ctx, srcDB, 0)
	require.NoError(t, err)
	require.NoError(t, srcStore.WriteBatch(ctx, []*usage.UsageEntry{
		{ID: "u-1", RequestID: "r-1", Timestamp: time.Now().UTC(), Model: "gpt-4o", Provider: "openai", Endpoint: "/v1/chat/completions"},
	}))
	require.NoError(t, src.Close())

	destPath := filepath.Join(t.TempDir(), "gomodel.db")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", destPath)

	var stdout, stderr bytes.Buffer
	err = Run(ctx, Options{Args: []string{"usage", "import", sourcePath}, Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err, stderr.String())
	assert.Contains(t, stdout.String(), "imported 1 usage rows")

	err = Run(ctx, Options{Args: []string{"usage", "import", destPath}, Stdout: io.Discard, Stderr: &stderr})
	require.ErrorContains(t, err, "this instance's own database")
}
