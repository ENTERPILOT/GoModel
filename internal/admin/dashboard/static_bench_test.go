package dashboard

import (
	"io/fs"
	"net/http"
	"path"
	"testing"

	"github.com/enterpilot/gomodel/internal/echotest"
)

// BenchmarkStatic serves the largest embedded JS asset to a client that
// accepts gzip, the common browser case on a cold dashboard load. allocs/op
// includes building the request and context.
func BenchmarkStatic(b *testing.B) {
	h, err := NewWithBasePath("/")
	if err != nil {
		b.Fatal(err)
	}
	matches, err := fs.Glob(content, "static/dist/assets/*.js")
	if err != nil || len(matches) == 0 {
		b.Fatalf("no embedded js asset: %v", err)
	}
	largest, size := "", int64(-1)
	for _, m := range matches {
		info, err := fs.Stat(content, m)
		if err != nil {
			b.Fatal(err)
		}
		if info.Size() > size {
			largest, size = m, info.Size()
		}
	}
	target := "/admin/static/assets/" + path.Base(largest)

	// Warm up so the timed loop measures steady state.
	warm := serveDiscard(b, h, target)
	if size > 1024 && warm.header.Get("Content-Encoding") != "gzip" {
		b.Fatalf("expected a gzip response for %s", target)
	}
	b.ReportAllocs()
	for b.Loop() {
		if w := serveDiscard(b, h, target); w.code != http.StatusOK {
			b.Fatalf("status %d", w.code)
		}
	}
	b.ReportMetric(float64(warm.n), "wire-B/op")
}

func serveDiscard(b *testing.B, h *Handler, target string) *discardWriter {
	w := &discardWriter{header: http.Header{}}
	c, _ := echotest.Get(b, target,
		echotest.WithHeader("Accept-Encoding", "gzip, deflate, br"),
		echotest.WithResponseWriter(func(http.ResponseWriter) http.ResponseWriter { return w }),
	)
	if err := h.Static(c); err != nil {
		b.Fatal(err)
	}
	return w
}

// discardWriter keeps the benchmark from measuring a recorder's buffer growth.
type discardWriter struct {
	header http.Header
	code   int
	n      int
}

func (w *discardWriter) Header() http.Header { return w.header }

func (w *discardWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

func (w *discardWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	w.n += len(p)
	return len(p), nil
}
