package mistral

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkReader serves s in fixed-size fragments to exercise the scanner across
// read boundaries.
type chunkReader struct {
	data []byte
	n    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(r.n, len(p), len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func (r *chunkReader) Close() error { return nil }

func TestThinkingChunkStream_FragmentedReads(t *testing.T) {
	upstream := &chunkReader{data: []byte(thinkingChunkSSE), n: 7}
	stream := newThinkingChunkStream(upstream)

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	out := string(body)
	assert.Contains(t, out, `"reasoning_content":"Let me compute. "`)
	assert.Contains(t, out, `"reasoning_content":"17*23=391."`)
	assert.Contains(t, out, `"content":"The answer is "`)
	assert.Contains(t, out, `"content":"391."`)
	assert.NotContains(t, out, `"type":"thinking"`)
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "stream must end with [DONE], got tail %q", out[max(0, len(out)-40):])
}

func TestThinkingChunkStream_PassesThroughUnrelatedEvents(t *testing.T) {
	const sse = ": comment\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"plain\"}}]}\n\ndata: [DONE]\n\n"
	stream := newThinkingChunkStream(&chunkReader{data: []byte(sse), n: 4096})

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, sse, string(body), "events without chunk arrays relay byte for byte")
}

func TestThinkingChunkStream_ReadAfterClose(t *testing.T) {
	stream := newThinkingChunkStream(&chunkReader{data: []byte("data: [DONE]\n\n"), n: 4096})
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	_, err = stream.Read(make([]byte, 8))
	assert.Equal(t, io.EOF, err)
}

func TestThinkingChunkStream_PropagatesUpstreamError(t *testing.T) {
	stream := newThinkingChunkStream(&failingReader{})
	_, err := io.ReadAll(stream)
	require.ErrorIs(t, err, errBoom)
	require.NoError(t, stream.Close())
}

var errBoom = io.ErrUnexpectedEOF

type failingReader struct{}

func (r *failingReader) Read([]byte) (int, error) { return 0, errBoom }
func (r *failingReader) Close() error             { return nil }
