package dashboard

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testJS    = []byte(strings.Repeat("export const greeting = 'hello dashboard';\n", 200))
	testCSS   = []byte(strings.Repeat(".card { color: red; margin: 0 auto; }\n", 200))
	testWoff2 = []byte(strings.Repeat("wOF2", 500))
	testPNG   = []byte(strings.Repeat("\x89PNG", 500))
	testTiny  = []byte("x")
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := newHandler(fstest.MapFS{
		"static/dist/index.html":              {Data: []byte(`<html><head></head><body></body></html>`)},
		"static/dist/assets/index-abc.js":     {Data: testJS},
		"static/dist/assets/index-abc.css":    {Data: testCSS},
		"static/dist/assets/tiny-abc.js":      {Data: testTiny},
		"static/dist/fonts/inter-latin.woff2": {Data: testWoff2},
		"static/dist/logo.png":                {Data: testPNG},
	}, "/", false)
	require.NoError(t, err)
	return h
}

func serveStaticWith(t *testing.T, h *Handler, target string, opts ...echotest.Option) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Get(t, target, opts...)
	require.NoError(t, h.Static(c))
	return rec
}

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return out
}

func TestStatic_GzipsTextAssets(t *testing.T) {
	tests := []struct {
		target      string
		want        []byte
		contentType string
	}{
		{target: "/admin/static/assets/index-abc.js", want: testJS, contentType: "text/javascript; charset=utf-8"},
		{target: "/admin/static/assets/index-abc.css", want: testCSS, contentType: "text/css; charset=utf-8"},
	}
	h := newTestHandler(t)
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			// Twice: the second request reuses the bytes compressed by the first.
			for range 2 {
				rec := serveStaticWith(t, h, tt.target, echotest.WithHeader("Accept-Encoding", "gzip, deflate, br"))

				require.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
				assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
				assert.Equal(t, tt.contentType, rec.Header().Get("Content-Type"))
				assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
				assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
				assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
				assert.Less(t, rec.Body.Len(), len(tt.want))
				assert.Equal(t, tt.want, gunzip(t, rec.Body.Bytes()))
			}
		})
	}
}

func TestStatic_ServesRawTextAssetsWithoutGzip(t *testing.T) {
	tests := []struct {
		name string
		opts []echotest.Option
	}{
		{name: "no accept-encoding"},
		{name: "other encodings only", opts: []echotest.Option{echotest.WithHeader("Accept-Encoding", "br, deflate")}},
		{name: "gzip refused", opts: []echotest.Option{echotest.WithHeader("Accept-Encoding", "gzip;q=0, br")}},
	}
	h := newTestHandler(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveStaticWith(t, h, "/admin/static/assets/index-abc.js", tt.opts...)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Encoding"))
			assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
			assert.Equal(t, "text/javascript; charset=utf-8", rec.Header().Get("Content-Type"))
			assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
			assert.Equal(t, testJS, rec.Body.Bytes())
		})
	}
}

func TestStatic_NeverGzipsCompressedOrTinyFiles(t *testing.T) {
	tests := []struct {
		target string
		want   []byte
	}{
		{target: "/admin/static/fonts/inter-latin.woff2", want: testWoff2},
		{target: "/admin/static/logo.png", want: testPNG},
		{target: "/admin/static/assets/tiny-abc.js", want: testTiny},
	}
	h := newTestHandler(t)
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			rec := serveStaticWith(t, h, tt.target, echotest.WithHeader("Accept-Encoding", "gzip"))

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Encoding"))
			assert.Equal(t, tt.want, rec.Body.Bytes())
		})
	}
}

// A range addresses the stored bytes, so it is served from them, not from
// the gzip encoding.
func TestStatic_RangeServesRawBytes(t *testing.T) {
	h := newTestHandler(t)

	rec := serveStaticWith(t, h, "/admin/static/assets/index-abc.js",
		echotest.WithHeader("Accept-Encoding", "gzip"),
		echotest.WithHeader("Range", "bytes=0-9"),
	)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Equal(t, testJS[:10], rec.Body.Bytes())
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{header: "", want: false},
		{header: "gzip", want: true},
		{header: "GZIP", want: true},
		{header: "x-gzip", want: true},
		{header: "gzip, deflate, br, zstd", want: true},
		{header: "br;q=1.0, gzip;q=0.8", want: true},
		{header: "gzip;q=0", want: false},
		{header: "gzip;q=0.000", want: false},
		{header: "deflate, br", want: false},
		{header: "gzipfoo", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			assert.Equal(t, tt.want, acceptsGzip(tt.header))
		})
	}
}
