package run

import (
	"bytes"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// detectTTY decides between the human-readable handler and JSON. It asks for
// os.ModeCharDevice, so every character device counts as a terminal — writing
// logs to /dev/null selects the coloured handler. That is deliberate, and
// these cases pin it.
func TestDetectTTY(t *testing.T) {
	t.Run("a writer that is not a file is never a terminal", func(t *testing.T) {
		require.False(t, detectTTY(&bytes.Buffer{}))
	})

	t.Run("a regular file is not a terminal", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "log")
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })

		require.False(t, detectTTY(file), "a log file must get the JSON handler")
	})

	t.Run("a character device counts as a terminal", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.DevNull is not a character device on Windows")
		}
		file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })

		require.True(t, detectTTY(file), "os.ModeCharDevice covers /dev/null, not only real terminals")
	})

	t.Run("a file that cannot be stat'd is not a terminal", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "log")
		require.NoError(t, err)
		require.NoError(t, file.Close())

		require.False(t, detectTTY(file), "a Stat error must fall back to JSON")
	})
}
