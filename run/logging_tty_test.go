package run

import (
	"bytes"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// detectTTY decides between the colourised handler and JSON. It asks
// golang.org/x/term rather than os.ModeCharDevice, and these cases pin the
// difference: /dev/null is a character device but not a terminal, so logs
// redirected there stay JSON. A true case needs a real terminal, which a test
// process does not have.
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

	t.Run("a character device that is not a terminal stays JSON", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.DevNull is not a character device on Windows")
		}
		file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })

		require.False(t, detectTTY(file), "/dev/null is a character device, but redirected logs must stay JSON")
	})

	t.Run("a closed file is not a terminal", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "log")
		require.NoError(t, err)
		require.NoError(t, file.Close())

		require.False(t, detectTTY(file))
	})
}
