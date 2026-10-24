package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

// fuzzBodyLimit is small so the fuzzer reaches the limit with short inputs.
const fuzzBodyLimit = 1 << 10

// Fuzz modes: send the fuzzed body as is under the fuzzed Content-Encoding, or
// treat it as plaintext and send it uncompressed, gzip- or zstd-compressed.
const (
	fuzzSendRaw uint8 = iota
	fuzzSendIdentity
	fuzzSendGzip
	fuzzSendZstd
	fuzzSendModes
)

// FuzzRequestDecompression feeds bodies and Content-Encoding values through
// the body limit and decompression middleware in server order. The handler
// must never see a body over the limit, and every rejection must be a client
// error the middleware chose on purpose. A body the test encodes itself must
// reach the handler byte for byte whenever it fits the limit.
func FuzzRequestDecompression(f *testing.F) {
	small := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	large := strings.Repeat("a", 8*fuzzBodyLimit)
	for _, seed := range []struct {
		encoding string
		body     []byte
	}{
		{"", []byte(small)},
		{"identity", []byte(small)},
		{"gzip", gzipBytes(f, small)},
		{"x-gzip", gzipBytes(f, small)},
		{"GZIP", gzipBytes(f, small)},
		{"zstd", zstdBytes(f, small)},
		{"gzip", gzipBytes(f, large)},
		{"zstd", zstdBytes(f, large)},
		{"gzip", gzipBytes(f, small)[:10]},
		{"zstd", []byte("not zstd")},
		{"gzip, zstd", gzipBytes(f, small)},
		{"br", []byte(small)},
		{"gzip", nil},
	} {
		f.Add(seed.encoding, seed.body, false, fuzzSendRaw)
		f.Add(seed.encoding, seed.body, true, fuzzSendRaw)
	}
	for _, plain := range []string{small, large, ""} {
		for mode := fuzzSendIdentity; mode < fuzzSendModes; mode++ {
			f.Add("", []byte(plain), true, mode)
		}
	}

	f.Fuzz(func(t *testing.T, encoding string, body []byte, chunked bool, mode uint8) {
		sent := body
		switch mode % fuzzSendModes {
		case fuzzSendIdentity:
			encoding = ""
		case fuzzSendGzip:
			encoding, sent = "gzip", gzipBytes(t, string(body))
		case fuzzSendZstd:
			encoding, sent = "zstd", zstdBytes(t, string(body))
		}

		var decoded []byte
		handler := func(c *echo.Context) error {
			read, err := io.ReadAll(c.Request().Body)
			if err != nil {
				// The body limit reports an uncompressed body over the limit
				// while the handler reads it.
				return err
			}
			decoded = read
			assert.LessOrEqual(t, len(read), fuzzBodyLimit, "handler saw a body over the limit")
			return c.NoContent(http.StatusNoContent)
		}
		chain := middleware.BodyLimit(fuzzBodyLimit)(requestDecompression(fuzzBodyLimit)(handler))

		c, rec := echotest.Post(t, "/v1/chat/completions", sent, echotest.WithHeader("Content-Encoding", encoding))
		if chunked {
			c.Request().ContentLength = -1
		}
		err := chain(c)
		status := rec.Code
		if err != nil {
			status = echo.StatusCode(err)
			require.NotZero(t, status, "unexpected error %v", err)
		}

		assert.LessOrEqual(t, len(decoded), fuzzBodyLimit)
		if mode%fuzzSendModes == fuzzSendRaw {
			assert.Contains(t, []int{
				http.StatusNoContent,
				http.StatusBadRequest,
				http.StatusRequestEntityTooLarge,
				http.StatusUnsupportedMediaType,
			}, status, "unexpected status for encoding %q", encoding)
			return
		}
		if len(sent) > fuzzBodyLimit || len(body) > fuzzBodyLimit {
			assert.Equal(t, http.StatusRequestEntityTooLarge, status)
			return
		}
		require.Equal(t, http.StatusNoContent, status, rec.Body.String())
		assert.Equal(t, string(body), string(decoded))
	})
}
