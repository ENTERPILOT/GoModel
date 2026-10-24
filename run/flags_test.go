package run

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCLI_AcceptsSingleAndDoubleDashVersion(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"--version"}} {
		opts, err := parseCLI("gomodel", args, io.Discard)
		require.NoError(t, err)
		require.True(t, opts.Version, "parseCLI(%v).Version = false, want true", args)
	}
}

func TestParseCLI_AcceptsSingleAndDoubleDashHealth(t *testing.T) {
	for _, args := range [][]string{{"-health"}, {"--health"}} {
		opts, err := parseCLI("gomodel", args, io.Discard)
		require.NoError(t, err)
		require.True(t, opts.Health, "parseCLI(%v).Health = false, want true", args)
	}
}

func TestParseCLI_RejectsUnknownFlags(t *testing.T) {
	_, err := parseCLI("gomodel", []string{"--helath"}, io.Discard)
	require.Error(t, err)
}

func TestParseCLI_RejectsRemovedDemoFlag(t *testing.T) {
	_, err := parseCLI("gomodel", []string{"--demo"}, io.Discard)
	require.Error(t, err)
}

func TestParseCLI_RejectsPositionalArgs(t *testing.T) {
	_, err := parseCLI("gomodel", []string{"--health", "extra"}, io.Discard)
	require.Error(t, err)
}

func TestExitCode(t *testing.T) {
	got := ExitCode(nil)
	require.Equal(t, 0, got)
	got = ExitCode(&usageError{err: fmt.Errorf("unexpected arguments")})
	require.Equal(t, 2, got)
	got = ExitCode(fmt.Errorf("boom"))
	require.Equal(t, 1, got)
}
