package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// errAfterReadCloser yields its data once, then fails — simulating a connection
// that drops mid-body after some bytes have arrived.
type errAfterReadCloser struct {
	data []byte
	err  error
	done bool
}

func (r *errAfterReadCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		r.done = true
	}
	return n, nil
}

func (r *errAfterReadCloser) Close() error { return nil }

func TestEnsureChatCompletionSSE_ConvertsBufferedJSON(t *testing.T) {
	// Upstream ignored stream:true and returned a buffered, non-SSE completion.
	body := `{"id":"x","object":"chat.completion","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hi there"}}]}`
	stream := io.NopCloser(strings.NewReader(body))

	got, err := io.ReadAll(EnsureChatCompletionSSE(stream))
	require.NoError(t, err)

	out := string(got)
	require.True(t, strings.HasPrefix(out, "data: {"), "expected SSE data framing, got %q", out)
	require.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "expected terminal done marker, got %q", out)
	require.Contains(t, out, `"object":"chat.completion.chunk"`)
	require.Contains(t, out, `"delta":`)
	require.NotContains(t, out, `"message":`)
}

func TestEnsureChatCompletionSSE_PassesThroughRealSSE(t *testing.T) {
	chunks := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	original := strings.Join([]string{string(chunks[0]), string(chunks[1]), string(chunks[2])}, "")
	stream := &chunkedReadCloser{chunks: chunks}

	got, err := io.ReadAll(EnsureChatCompletionSSE(stream))
	require.NoError(t, err)
	require.Equal(t, original, string(got))
}

func TestEnsureChatCompletionSSE_PassesThroughSSEWithLeadingComment(t *testing.T) {
	// Providers like OpenRouter emit a leading ": ... PROCESSING" comment line.
	body := ": OPENROUTER PROCESSING\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"
	stream := io.NopCloser(strings.NewReader(body))

	got, err := io.ReadAll(EnsureChatCompletionSSE(stream))
	require.NoError(t, err)
	require.Equal(t, body, string(got))
}

func TestEnsureChatCompletionSSE_PreservesPartialBodyOnReadError(t *testing.T) {
	// Upstream began a buffered JSON body, then the connection dropped mid-read.
	// The partial content must still reach the client, followed by [DONE].
	partial := `{"id":"x","choices":[{"message":{"content":"Hel`
	stream := &errAfterReadCloser{data: []byte(partial), err: io.ErrUnexpectedEOF}

	got, err := io.ReadAll(EnsureChatCompletionSSE(stream))
	require.NoError(t, err)

	out := string(got)
	require.Contains(t, out, "Hel")
	require.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "expected terminal done marker, got %q", out)
}

func TestEnsureChatCompletionSSE_NilStream(t *testing.T) {
	require.Nil(t, EnsureChatCompletionSSE(nil))
}
