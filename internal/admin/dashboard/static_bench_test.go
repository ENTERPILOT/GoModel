package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/labstack/echo/v5"
)

// BenchmarkStatic serves the largest embedded JS asset to a client that
// accepts gzip, the common browser case on a cold dashboard load.
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
	e := echo.New()
	// Warm up so the timed loop measures steady state.
	wire := w0(h, e, target)
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		w := &discardWriter{header: http.Header{}}
		if err := h.Static(e.NewContext(req, w)); err != nil {
			b.Fatal(err)
		}
		if w.code != http.StatusOK {
			b.Fatalf("status %d", w.code)
		}
	}
	b.ReportMetric(float64(wire), "wire-B/op")
}

// w0 returns the body size one request puts on the wire.
func w0(h *Handler, e *echo.Echo, target string) int {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	w := &discardWriter{header: http.Header{}}
	_ = h.Static(e.NewContext(req, w))
	return w.n
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
