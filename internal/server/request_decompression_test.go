package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
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

// countingReader counts the compressed bytes the gateway has read.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// incompressibleJSON is a JSON body whose compressed size is close to its
// decoded size, so the compressed bytes read show how much was decoded.
func incompressibleJSON(t *testing.T, size int) string {
	t.Helper()
	random := make([]byte, size*3/4)
	_, _ = rand.NewChaCha8([32]byte{}).Read(random)
	return `{"model":"gpt-4o-mini","padding":"` + base64.StdEncoding.EncodeToString(random) + `"}`
}

// Before authentication only the head of a compressed body is decoded, so a
// caller without credentials cannot make the gateway expand a small body into
// the whole body limit.
func TestRequestDecompression_DecodesOnlyTheHeadBeforeAuth(t *testing.T) {
	body := incompressibleJSON(t, 4<<20)
	encoders := map[string]func(*testing.T, string) []byte{"gzip": gzipBytes, "zstd": zstdBytes}
	for encoding, encode := range encoders {
		compressed := encode(t, body)
		for _, path := range []string{"/v1/chat/completions", "/p/openai/chat/completions"} {
			t.Run(encoding+" "+path, func(t *testing.T) {
				srv := New(&mockProvider{passthroughResponse: okPassthroughResponse()}, &Config{MasterKey: "master"})
				counter := &countingReader{r: bytes.NewReader(compressed)}
				req := httptest.NewRequest(http.MethodPost, path, counter)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Content-Encoding", encoding)
				rec := httptest.NewRecorder()

				srv.ServeHTTP(rec, req)

				require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
				assert.Less(t, counter.read, 1<<20, "read %d of %d compressed bytes before auth", counter.read, len(compressed))
			})
		}
	}
}

// A body longer than the head is decoded in full once the caller has
// authenticated, and reaches the provider unchanged.
func TestRequestDecompression_DecodesLongBodiesAfterAuth(t *testing.T) {
	state := strings.Repeat("I was charged twice. ", 10_000)
	body := `{"model":"kev/kev-latest","state":"` + state + `","questions":` + systemOneQuestions + `}`
	require.Greater(t, len(body), int(requestDecodeAhead))
	encoders := map[string]func(*testing.T, string) []byte{"gzip": gzipBytes, "zstd": zstdBytes}
	for encoding, encode := range encoders {
		t.Run(encoding, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			srv := New(provider, &Config{MasterKey: "master"})
			req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(encode(t, body)))
			req.Header.Set("Authorization", "Bearer master")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", encoding)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, state, forwardedSystemOneBody(t, provider)["state"])
		})
	}
}

// A decoded body over the limit is a 413 once the caller has authenticated;
// before that, the caller only learns it is not authenticated.
func TestRequestDecompression_LimitsTheDecodedBodyAfterAuth(t *testing.T) {
	body := `{"model":"kev/kev-latest","state":"` + strings.Repeat("a", 200<<10) + `","questions":{}}`
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "authenticated", token: "master", want: http.StatusRequestEntityTooLarge},
		{name: "unauthenticated", want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			srv := New(provider, &Config{MasterKey: "master", BodySizeLimit: "100K"})
			req := httptest.NewRequest(http.MethodPost, "/v1/systemone", bytes.NewReader(gzipBytes(t, body)))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Nil(t, provider.lastPassthroughReq)
		})
	}
}

// An opaque passthrough body is still read in full for its model when it is
// compressed and longer than the head: the model after a long value is the
// one checked, and a repeated one is refused.
func TestRequestDecompression_PassthroughModelBeyondTheHead(t *testing.T) {
	padding := strings.Repeat("x", int(requestDecodeAhead))
	tests := []struct {
		name      string
		body      string
		wantModel string
		want      int
	}{
		{name: "model after a long value", body: `{"padding":"` + padding + `","model":"gpt-4o-mini"}`, wantModel: "gpt-4o-mini", want: http.StatusOK},
		{name: "repeated model", body: `{"model":"gpt-4o-mini","padding":"` + padding + `","model":"gpt-5"}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{passthroughResponse: okPassthroughResponse()}
			authorizer := &recordingModelAuthorizer{}
			srv := New(mock, &Config{MasterKey: "master", ModelAuthorizer: authorizer})
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", bytes.NewReader(gzipBytes(t, tt.body)))
			req.Header.Set("Authorization", "Bearer master")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want != http.StatusOK {
				assert.Nil(t, mock.lastPassthroughReq)
				return
			}
			assert.Equal(t, tt.wantModel, authorizer.lastSelector.Model)
			require.NotNil(t, mock.lastPassthroughReq)
			forwarded, err := io.ReadAll(mock.lastPassthroughReq.Body)
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(forwarded))
		})
	}
}

// A compressed body decoded after auth is recorded as too large to capture
// exactly when its decoded length would have been.
func TestFinishRequestDecompression_RecordsAnUncapturedBody(t *testing.T) {
	tests := []struct {
		name string
		size int
		want bool
	}{
		{name: "within the audit capture", size: 100 << 10},
		{name: "over the audit capture", size: 2 << 20, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const limit = 10 << 20
			e := echo.New()
			e.Use(requestDecompression(limit), RequestSnapshotCapture(), finishRequestDecompression(limit))
			var notCaptured, seen bool
			e.POST("/v1/chat/completions", func(c *echo.Context) error {
				snapshot := core.GetRequestSnapshot(c.Request().Context())
				seen = snapshot != nil
				notCaptured = seen && snapshot.BodyNotCaptured
				return c.NoContent(http.StatusNoContent)
			})
			body := `{"model":"gpt-4o-mini","padding":"` + strings.Repeat("a", tt.size) + `"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(gzipBytes(t, body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			require.True(t, seen)
			assert.Equal(t, tt.want, notCaptured)
		})
	}
}
