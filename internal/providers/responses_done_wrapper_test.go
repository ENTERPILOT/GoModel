package providers

import (
	"io"
	"strings"
	"testing"
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
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Fatalf("expected stream to end with done marker, got %q", got)
	}
	if strings.Count(got, "[DONE]") != 1 {
		t.Fatalf("expected exactly one done marker, got %q", got)
	}
}

func TestEnsureResponsesDone_InsertsEventSeparatorBeforeDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	if got != want {
		t.Fatalf("expected wrapper to terminate the completed SSE event before [DONE], got %q", got)
	}
}

func TestEnsureResponsesDone_PreservesExistingDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	if strings.Count(got, "[DONE]") != 1 {
		t.Fatalf("expected existing done marker to be preserved without duplication, got %q", got)
	}
}

func TestEnsureResponsesDone_PreservesSplitDoneMarker(t *testing.T) {
	stream := &chunkedReadCloser{
		chunks: [][]byte{
			[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DO"),
			[]byte("NE]\n\n"),
		},
	}

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	if got != want {
		t.Fatalf("expected split done marker to pass through unchanged, got %q", got)
	}
	if strings.Count(got, "[DONE]") != 1 {
		t.Fatalf("expected exactly one done marker, got %q", got)
	}
}

func TestEnsureResponsesDone_DoesNotMaskIncompleteStream(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	if strings.Contains(got, "[DONE]") {
		t.Fatalf("expected incomplete stream to remain incomplete, got %q", got)
	}
}

func TestEnsureResponsesDone_PreservesEOFTerminatedDoneMarker(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n"))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n"
	if got != want {
		t.Fatalf("expected EOF-terminated done marker to pass through unchanged, got %q", got)
	}
	if strings.Count(got, "[DONE]") != 1 {
		t.Fatalf("expected exactly one done marker, got %q", got)
	}
}

func TestEnsureResponsesDone_CompletesPartialDonePrefixWithoutDuplication(t *testing.T) {
	stream := &chunkedReadCloser{
		chunks: [][]byte{
			[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"),
			[]byte("data: [DO"),
		},
	}

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	want := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	if got != want {
		t.Fatalf("expected partial done prefix to be completed in place, got %q", got)
	}
}

func TestEnsureResponsesDone_IgnoresDoneSubstringInsideJSONPayload(t *testing.T) {
	stream := io.NopCloser(strings.NewReader(
		"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"data: [DONE]\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\"}\n",
	))

	data, err := io.ReadAll(EnsureResponsesDone(stream))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	got := string(data)
	if !strings.Contains(got, "\"delta\":\"data: [DONE]\"") {
		t.Fatalf("expected model text containing done literal to be preserved, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\ndata: [DONE]\n\n") {
		t.Fatalf("expected a real terminal done event at EOF, got %q", got)
	}
}
