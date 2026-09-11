package server

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func readChained(t *testing.T, stream string, id string, oneByte bool) string {
	t.Helper()
	var src io.Reader = strings.NewReader(stream)
	if oneByte {
		src = iotest.OneByteReader(src)
	}
	out, err := io.ReadAll(withPreviousResponseID(io.NopCloser(src), id))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(out)
}

func TestWithPreviousResponseID(t *testing.T) {
	const (
		created   = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\",\"status\":\"in_progress\"},\"sequence_number\":0}\n\n"
		delta     = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"say \\\"response\\\":\",\"sequence_number\":1}\n\n"
		completed = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"previous_response_id\":null,\"status\":\"completed\"},\"sequence_number\":2}\n\n"
		native    = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"previous_response_id\":\"resp_upstream\"}}\n\n"
		comment   = ": keep-alive\n\n"
		done      = "data: [DONE]\n\n"
	)
	named := func(event string) string {
		return "event: " + event + "\ndata: "
	}
	tests := []struct {
		name   string
		stream string
		id     string
		want   []string // substrings, in order
		same   bool     // output must equal the input byte for byte
	}{
		{name: "unchained stream is untouched", stream: created + delta + completed + done, id: "", same: true},
		{name: "names the predecessor on response objects", stream: comment + created + delta + completed + done, id: "resp_1",
			want: []string{comment, named("response.created"), `"previous_response_id":"resp_1"`, delta, named("response.completed"), `"previous_response_id":"resp_1"`, done}},
		{name: "keeps a predecessor the provider named", stream: native + done, id: "resp_1", same: true},
		{name: "unterminated trailing event is still named", stream: strings.TrimSuffix(completed, "\n\n"), id: "resp_1",
			want: []string{`"previous_response_id":"resp_1"`, `"status":"completed"`}},
		{name: "malformed JSON passes through", stream: "data: {\"response\":\n\n" + done, id: "resp_1", same: true},
	}
	for _, tc := range tests {
		for _, oneByte := range []bool{false, true} {
			got := readChained(t, tc.stream, tc.id, oneByte)
			if tc.same {
				if got != tc.stream {
					t.Errorf("%s (one byte reads %v): got %q, want the input unchanged", tc.name, oneByte, got)
				}
				continue
			}
			rest := got
			for _, w := range tc.want {
				i := strings.Index(rest, w)
				if i < 0 {
					t.Errorf("%s (one byte reads %v): missing %q in order in %q", tc.name, oneByte, w, got)
					break
				}
				rest = rest[i+len(w):]
			}
		}
	}
}
