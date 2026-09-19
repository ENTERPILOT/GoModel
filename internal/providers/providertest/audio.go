package providertest

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// AudioBytes returns the payload of an audio response. Providers relay a body
// the upstream is still producing (synthesized speech, a stream=true
// transcript) instead of buffering it, so a test asserting on the bytes drains
// and closes the stream here rather than reading Data directly.
func AudioBytes(t *testing.T, resp *core.AudioResponse) []byte {
	t.Helper()
	require.NotNil(t, resp)
	if resp.Stream == nil {
		return resp.Data
	}
	require.Empty(t, resp.Data, "a relayed audio response carries no buffered Data")
	defer func() { _ = resp.Stream.Close() }()
	data, err := io.ReadAll(resp.Stream)
	require.NoError(t, err)
	return data
}
