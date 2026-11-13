package server

import (
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/usage"
)

// sliceSink is a connection that keeps the slices written to it, without
// copying them, so a test can tell whose memory they are.
type sliceSink struct {
	header http.Header
	writes [][]byte
	bytes  int
}

func (s *sliceSink) Header() http.Header { return s.header }
func (s *sliceSink) WriteHeader(int)     {}
func (s *sliceSink) Write(p []byte) (int, error) {
	if s.writes != nil {
		s.writes = append(s.writes, p)
	}
	s.bytes += len(p)
	return len(p), nil
}

// costWriterInto returns a writer of JSON bodies into sink, for a request
// that cost $0.5.
func costWriterInto(tb testing.TB, sink *sliceSink) *responseCostWriter {
	tb.Helper()
	ctx, report := usage.WithCostReport(tb.Context(), false)
	usage.ReportCost(ctx, nil, &usage.UsageEntry{TotalCost: new(0.5)})
	sink.header = http.Header{"Content-Type": {"application/json"}}
	return &responseCostWriter{ResponseWriter: sink, report: report, editsJSON: true}
}

// The bytes around a usage object are written from the caller's own buffer:
// the connection receives slices of it, not copies.
func TestResponseCostWriter_RelaysTheCallersBytes(t *testing.T) {
	body := []byte(`{"id":"x","usage":{"total_tokens":3},"object":"chat.completion"}`)
	sink := &sliceSink{writes: [][]byte{}}
	w := costWriterInto(t, sink)

	_, err := w.Write(body)
	require.NoError(t, err)

	require.Len(t, sink.writes, 3)
	assert.Same(t, &body[0], &sink.writes[0][0], "the bytes before the usage object")
	assert.JSONEq(t, `{"total_tokens":3,"cost":0.5}`, string(sink.writes[1]))
	tail := `,"object":"chat.completion"}`
	assert.Same(t, &body[len(body)-len(tail)], &sink.writes[2][0], "the bytes after it")
}

// Relaying costs no allocation per write: not for a body that is no JSON
// object, which is not even scanned after its first byte, and not for the
// members of an object around its usage.
func TestResponseCostWriter_RelaysWithoutAllocating(t *testing.T) {
	chunk := []byte(strings.Repeat(`{"embedding":[0.1,0.2,0.3]},`, 512))
	tests := []struct {
		name  string
		start string
	}{
		{name: "top-level array", start: `[`},
		{name: "object without usage so far", start: `{"data":[`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &sliceSink{}
			w := costWriterInto(t, sink)
			_, err := w.Write([]byte(tt.start))
			require.NoError(t, err)

			allocs := testing.AllocsPerRun(50, func() { _, _ = w.Write(chunk) })

			assert.Zero(t, allocs)
			assert.Equal(t, tt.start != `[`, w.body != nil, "only an object is scanned")
		})
	}
}

// A body written in one piece is not copied: an 8 MiB body costs the writer a
// fraction of that in memory, where a copy would cost all of it again.
func TestResponseCostWriter_LargeBodyInOneWriteIsNotCopied(t *testing.T) {
	const size = 8 << 20
	body := []byte(`{"data":"` + strings.Repeat("a", size) + `","usage":{"total_tokens":3}}`)
	for _, start := range []string{"", "[", " "} {
		sink := &sliceSink{}
		w := costWriterInto(t, sink)
		payload := append([]byte(start), body...)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		_, err := w.Write(payload)

		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size/8), "start %q", start)
		assert.GreaterOrEqual(t, sink.bytes, len(payload))
	}
}

// BenchmarkResponseCostWriter measures what relaying a large body costs: the
// scan of an object, and nothing for a body that is not one.
func BenchmarkResponseCostWriter(b *testing.B) {
	object := []byte(`{"data":"` + strings.Repeat("a", 1<<20) + `","usage":{"total_tokens":3}}`)
	bodies := []struct {
		name string
		body []byte
	}{
		{name: "object", body: object},
		{name: "array", body: append([]byte("["), append(object, ']')...)},
	}
	for _, bb := range bodies {
		b.Run(bb.name, func(b *testing.B) {
			b.SetBytes(int64(len(bb.body)))
			b.ReportAllocs()
			for b.Loop() {
				w := costWriterInto(b, &sliceSink{})
				if _, err := w.Write(bb.body); err != nil {
					b.Fatal(err)
				}
				w.finish()
			}
		})
	}
}
