package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type chunkedReadCloser struct {
	chunks [][]byte
	index  int
}

func (r *chunkedReadCloser) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}

	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

func (r *chunkedReadCloser) Close() error {
	return nil
}

func TestEnsureResponsesDone_AppendsDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	require.True(t, strings.HasSuffix(got, "data: [DONE]\n\n"), "expected stream to end with done marker, got %q", got)
	require.Equal(t, 1, strings.Count(got, "[DONE]"), "expected exactly one done marker, got %q", got)
}

func TestEnsureResponsesDone_AppendsDoneMarkerAfterIncomplete(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	require.True(t, strings.HasSuffix(got, "data: [DONE]\n\n"), "expected stream to end with done marker, got %q", got)
	require.Equal(t, 1, strings.Count(got, "[DONE]"), "expected exactly one done marker, got %q", got)
}

func TestEnsureResponsesDone_InsertsEventSeparatorBeforeDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	require.Equal(t, want, got)
}

func TestEnsureResponsesDone_HandlesCompletedDataLineAtEOFWithoutTrailingNewline(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	require.Equal(t, want, got)
}

func TestEnsureResponsesDone_PreservesExistingDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	require.Equal(t, 1, strings.Count(got, "[DONE]"), "expected existing done marker to be preserved without duplication, got %q", got)
}

func TestEnsureResponsesDone_PreservesSplitDoneMarker(t *testing.T) {
	stream := &chunkedReadCloser{
		chunks: [][]byte{
			[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DO"),
			[]byte("NE]\n\n"),
		},
	}

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	require.Equal(t, want, got)
	require.Equal(t, 1, strings.Count(got, "[DONE]"), "expected exactly one done marker, got %q", got)
}

func TestEnsureResponsesDone_DoesNotMaskIncompleteStream(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	require.NotContains(t, got, "[DONE]", "incomplete stream should remain incomplete")
}

func TestEnsureResponsesDone_PreservesEOFTerminatedDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n"
	require.Equal(t, want, got)
	require.Equal(t, 1, strings.Count(got, "[DONE]"), "expected exactly one done marker, got %q", got)
}

func TestEnsureResponsesDone_CompletesPartialDonePrefixWithoutDuplication(t *testing.T) {
	stream := &chunkedReadCloser{
		chunks: [][]byte{
			[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"),
			[]byte("data: [DO"),
		},
	}

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	require.Equal(t, want, got)
}

func TestEnsureResponsesDone_IgnoresDoneSubstringInsideJSONPayload(t *testing.T) {
	stream := io.NopCloser(strings.NewReader(
		"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"data: [DONE]\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\"}\n",
	))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	require.NoError(t, err)

	got := string(data)
	require.Contains(t, got, "\"delta\":\"data: [DONE]\"")
	require.True(t, strings.HasSuffix(got, "\n\ndata: [DONE]\n\n"), "expected a real terminal done event at EOF, got %q", got)
}
