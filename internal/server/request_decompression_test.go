package server

import (
	"bytes"
	"compress/gzip"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gzipBytes(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func zstdBytes(t *testing.T, body string) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer enc.Close()
	return enc.EncodeAll([]byte(body), nil)
}

func postEncoded(t *testing.T, srv *Server, path, encoding string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", encoding)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// The TypeSafe SDKs zstd-compress bodies of 1 KiB or more, and other clients
// gzip them: a compressed body is decoded before any handler reads it, and
// reaches the provider uncompressed.
func TestRequestDecompression_DecodesGzipAndZstd(t *testing.T) {
	body := systemOneBody("kev/kev-latest")
	encoders := map[string]func(*testing.T, string) []byte{
		"gzip":   gzipBytes,
		"x-gzip": gzipBytes,
		"zstd":   zstdBytes,
	}
	for encoding, encode := range encoders {
		t.Run(encoding, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			srv := New(provider, &Config{})

			rec := postEncoded(t, srv, "/v1/systemone", encoding, encode(t, body))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			forwarded := forwardedSystemOneBody(t, provider)
			assert.Equal(t, "I was charged twice for card 4111.", forwarded["state"])
			assert.Empty(t, provider.lastPassthroughReq.Headers.Get("Content-Encoding"))
		})
	}
}

// The body limit applies to the decoded body too, or a small compressed body
// could expand without bound.
func TestRequestDecompression_LimitsTheDecodedBody(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	srv := New(provider, &Config{BodySizeLimit: "1K"})
	body := `{"model":"kev/kev-latest","state":"` + strings.Repeat("a", 4096) + `","questions":{}}`
	compressed := gzipBytes(t, body)
	require.Less(t, len(compressed), 1024, "the compressed body must fit the limit for the test to mean anything")

	rec := postEncoded(t, srv, "/v1/systemone", "gzip", compressed)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	assert.Nil(t, provider.lastPassthroughReq)
}

// A compressed body over the limit is a 413 too, not a decoding error.
func TestRequestDecompression_LimitsTheCompressedBody(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	srv := New(provider, &Config{BodySizeLimit: "1K"})
	random := make([]byte, 4096)
	_, _ = rand.NewChaCha8([32]byte{}).Read(random)
	compressed := gzipBytes(t, string(random))
	require.Greater(t, len(compressed), 1024, "random bytes do not compress below the limit")
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(compressed))
	req.ContentLength = -1 // chunked, so only reading the body finds it too large
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}

func TestRequestDecompression_RejectsUnsupportedAndMalformedBodies(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		extra    string // a second Content-Encoding header field
		body     []byte
		want     int
	}{
		{name: "brotli", encoding: "br", body: []byte("x"), want: http.StatusUnsupportedMediaType},
		{name: "two layers", encoding: "gzip, zstd", body: []byte("x"), want: http.StatusUnsupportedMediaType},
		{name: "two header fields", encoding: "gzip", extra: "identity", body: gzipBytes(t, "{}"), want: http.StatusUnsupportedMediaType},
		{name: "malformed gzip", encoding: "gzip", body: []byte("not gzip"), want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			srv := New(provider, &Config{})

			req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", tt.encoding)
			if tt.extra != "" {
				req.Header.Add("Content-Encoding", tt.extra)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Nil(t, provider.lastPassthroughReq)
		})
	}
}

// A gzip header larger than the limit fails while the reader is created, and
// is still reported as too large rather than as broken gzip.
func TestRequestDecompression_OversizedGzipHeaderIs413(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	srv := New(provider, &Config{BodySizeLimit: "1K"})
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Extra = bytes.Repeat([]byte("x"), 4096)
	_, err := w.Write([]byte("{}"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(buf.Bytes()))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}
