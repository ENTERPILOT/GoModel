package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

// fuzzBodyLimit is small so the fuzzer reaches the limit with short inputs.
const fuzzBodyLimit = 1 << 10

// FuzzRequestDecompression feeds arbitrary bodies and Content-Encoding values
// through the body limit and decompression middleware in server order. The
// handler must never see a body over the limit, and every rejection must be a
// client error the middleware chose on purpose.
func FuzzRequestDecompression(f *testing.F) {
	small := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	large := bytes.Repeat([]byte("a"), 8*fuzzBodyLimit)
	for _, seed := range []struct {
		encoding string
		body     []byte
	}{
		{"", small},
		{"identity", small},
		{"gzip", gzipSeed(f, small)},
		{"x-gzip", gzipSeed(f, small)},
		{"GZIP", gzipSeed(f, small)},
		{"zstd", zstdSeed(f, small)},
		{"gzip", gzipSeed(f, large)},
		{"zstd", zstdSeed(f, large)},
		{"gzip", gzipSeed(f, small)[:10]},
		{"zstd", []byte("not zstd")},
		{"gzip, zstd", gzipSeed(f, small)},
		{"br", small},
		{"gzip", nil},
	} {
		f.Add(seed.encoding, seed.body, false)
		f.Add(seed.encoding, seed.body, true)
	}

	f.Fuzz(func(t *testing.T, encoding string, body []byte, chunked bool) {
		var decodedLen int
		handler := func(c *echo.Context) error {
			decoded, err := io.ReadAll(c.Request().Body)
			if err != nil {
				// The body limit reports an uncompressed body over the limit
				// while the handler reads it.
				return err
			}
			decodedLen = len(decoded)
			assert.LessOrEqual(t, len(decoded), fuzzBodyLimit, "handler saw a body over the limit")
			return c.NoContent(http.StatusNoContent)
		}
		chain := middleware.BodyLimit(fuzzBodyLimit)(requestDecompression(fuzzBodyLimit)(handler))

		c, rec := echotest.Post(t, "/v1/chat/completions", body, echotest.WithHeader("Content-Encoding", encoding))
		if chunked {
			c.Request().ContentLength = -1
		}
		err := chain(c)
		status := rec.Code
		if err != nil {
			status = echo.StatusCode(err)
			require.NotZero(t, status, "unexpected error %v", err)
		}

		assert.Contains(t, []int{
			http.StatusNoContent,
			http.StatusBadRequest,
			http.StatusRequestEntityTooLarge,
			http.StatusUnsupportedMediaType,
		}, status, "unexpected status for encoding %q", encoding)
		assert.LessOrEqual(t, decodedLen, fuzzBodyLimit)
	})
}

func gzipSeed(f *testing.F, body []byte) []byte {
	f.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(body)
	require.NoError(f, err)
	require.NoError(f, w.Close())
	return buf.Bytes()
}

func zstdSeed(f *testing.F, body []byte) []byte {
	f.Helper()
	enc, err := zstd.NewWriter(nil)
	require.NoError(f, err)
	defer enc.Close()
	return enc.EncodeAll(body, nil)
}
