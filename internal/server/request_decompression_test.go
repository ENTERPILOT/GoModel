package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
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

// countingReader counts the request body bytes the gateway has read.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// postWithCredential posts body to the full server, with the master key when
// token is set and gzip-compressed when compress is.
func postWithCredential(t *testing.T, srv *Server, path, token string, body io.Reader, compress string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if compress != "" {
		req.Header.Set("Content-Encoding", compress)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// A request without credentials is rejected before its body is read, so it
// cannot make the gateway decompress or buffer anything.
func TestRequestBody_NotReadBeforeAuth(t *testing.T) {
	body := `{"model":"gpt-4o-mini","padding":"` + strings.Repeat("a", 4<<20) + `"}`
	bodies := map[string][]byte{"": []byte(body), "gzip": gzipBytes(t, body), "zstd": zstdBytes(t, body)}
	for encoding, encoded := range bodies {
		for _, path := range []string{"/v1/chat/completions", "/p/openai/chat/completions"} {
			t.Run(encoding+" "+path, func(t *testing.T) {
				srv := New(&mockProvider{passthroughResponse: okPassthroughResponse()}, &Config{MasterKey: "master"})
				counter := &countingReader{r: bytes.NewReader(encoded)}

				rec := postWithCredential(t, srv, path, "", counter, encoding)

				require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
				assert.Zero(t, counter.read)
			})
		}
	}
}

// A wrong credential is rejected before the body is read too, with the master
// key and with managed keys alike.
func TestRequestBody_NotReadWithInvalidCredential(t *testing.T) {
	body := zstdBytes(t, `{"model":"gpt-4o-mini","padding":"`+strings.Repeat("a", 4<<20)+`"}`)
	configs := map[string]*Config{
		"master key": {MasterKey: "master"},
		"managed keys": {Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_valid": "key-1"},
		}},
	}
	for name, cfg := range configs {
		t.Run(name, func(t *testing.T) {
			srv := New(&mockProvider{passthroughResponse: okPassthroughResponse()}, cfg)
			counter := &countingReader{r: bytes.NewReader(body)}

			rec := postWithCredential(t, srv, "/v1/chat/completions", "wrong-key", counter, "zstd")

			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			assert.Zero(t, counter.read)
		})
	}
}

// Once the caller is authenticated, a long compressed body is decoded in full
// and reaches the provider unchanged.
func TestRequestDecompression_DecodesLongBodiesAfterAuth(t *testing.T) {
	state := strings.Repeat("I was charged twice. ", 10_000)
	body := `{"model":"kev/kev-latest","state":"` + state + `","questions":` + systemOneQuestions + `}`
	encoders := map[string]func(*testing.T, string) []byte{"gzip": gzipBytes, "zstd": zstdBytes}
	for encoding, encode := range encoders {
		t.Run(encoding, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			srv := New(provider, &Config{MasterKey: "master"})

			rec := postWithCredential(t, srv, "/v1/systemone", "master", bytes.NewReader(encode(t, body)), encoding)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, state, forwardedSystemOneBody(t, provider)["state"])
		})
	}
}

// A decoded body over the limit is a 413 for an authenticated caller; one
// without credentials only learns that it is not authenticated.
func TestRequestDecompression_LimitsTheDecodedBodyAfterAuth(t *testing.T) {
	body := gzipBytes(t, `{"model":"kev/kev-latest","state":"`+strings.Repeat("a", 200<<10)+`","questions":{}}`)
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

			rec := postWithCredential(t, srv, "/v1/systemone", tt.token, bytes.NewReader(body), "gzip")

			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Nil(t, provider.lastPassthroughReq)
		})
	}
}

// An opaque passthrough body is read in full for its model after auth, also
// when it is compressed: the model after a long value is the one checked, and
// a repeated one is refused.
func TestRequestBody_PassthroughModelBeyondThePeek(t *testing.T) {
	padding := strings.Repeat("x", int(requestSelectorPeekLimit))
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
		for _, encoding := range []string{"", "gzip"} {
			t.Run(tt.name+" "+encoding, func(t *testing.T) {
				mock := &mockProvider{passthroughResponse: okPassthroughResponse()}
				authorizer := &recordingModelAuthorizer{}
				srv := New(mock, &Config{MasterKey: "master", ModelAuthorizer: authorizer})
				body := []byte(tt.body)
				if encoding != "" {
					body = gzipBytes(t, tt.body)
				}

				rec := postWithCredential(t, srv, "/p/openai/chat/completions", "master", bytes.NewReader(body), encoding)

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
}

// The audit entry records the decoded body of an authenticated request, says
// why a body too large to capture is missing, and holds no body for a
// request rejected before its body was read.
func TestRequestBody_Audit(t *testing.T) {
	small := `{"model":"gpt-4o-mini","messages":[]}`
	large := `{"model":"gpt-4o-mini","padding":"` + strings.Repeat("a", 2<<20) + `"}`
	tests := []struct {
		name        string
		body        string
		encoding    string
		token       string
		wantStatus  int
		wantBody    bool
		wantTooLong bool
	}{
		{name: "compressed", body: small, encoding: "gzip", token: "master", wantStatus: http.StatusOK, wantBody: true},
		{name: "compressed over the audit capture", body: large, encoding: "gzip", token: "master", wantStatus: http.StatusOK, wantTooLong: true},
		{name: "uncompressed over the audit capture", body: large, token: "master", wantStatus: http.StatusOK, wantTooLong: true},
		{name: "unauthenticated", body: small, wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
			srv := New(&mockProvider{passthroughResponse: okPassthroughResponse()}, &Config{MasterKey: "master", AuditLogger: auditLogger})
			body := []byte(tt.body)
			if tt.encoding != "" {
				body = gzipBytes(t, tt.body)
			}

			rec := postWithCredential(t, srv, "/p/openai/chat/completions", tt.token, bytes.NewReader(body), tt.encoding)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Len(t, auditLogger.entries, 1)
			data := auditLogger.entries[0].Data
			require.NotNil(t, data)
			assert.Equal(t, tt.wantBody, data.RequestBody != nil)
			assert.Equal(t, tt.wantTooLong, data.RequestBodyTooBigToHandle)
		})
	}
}
