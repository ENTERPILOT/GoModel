package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxZstdWindowBytes bounds the memory a zstd request body may make the
// decoder reserve, matching the window TypeSafe's API accepts.
const maxZstdWindowBytes = 8 << 20

// requestDecompression decodes request bodies sent with Content-Encoding gzip
// (or x-gzip) or zstd, so every handler, the audit capture, and passthrough
// see the JSON the client meant. The TypeSafe SDKs compress bodies of 1 KiB
// or more with zstd by default. The decoded body is held to limit as well
// (413): the body limit middleware only sees the compressed bytes, and a
// small compressed body can expand without bound. Other encodings, or more
// than one layer, are rejected with 415.
func requestDecompression(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
			if encoding == "" || encoding == "identity" || req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}
			var decoded io.ReadCloser
			switch encoding {
			case "gzip", "x-gzip":
				reader, err := gzip.NewReader(req.Body)
				if err != nil {
					return handleError(c, core.NewInvalidRequestError("invalid gzip request body: "+err.Error(), err))
				}
				decoded = reader
			case "zstd":
				reader, err := zstd.NewReader(req.Body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindowBytes))
				if err != nil {
					return handleError(c, core.NewInvalidRequestError("invalid zstd request body: "+err.Error(), err))
				}
				decoded = reader.IOReadCloser()
			default:
				return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusUnsupportedMediaType,
					"unsupported Content-Encoding "+encoding+"; send gzip, zstd, or an uncompressed body", nil))
			}
			// Decode eagerly: the decoded body is bounded by the limit either
			// way, and reading it here reports an oversized or corrupt body
			// with its own status before any handler runs.
			body, err := io.ReadAll(io.LimitReader(decoded, limit+1))
			_ = decoded.Close()
			_ = req.Body.Close()
			if err != nil && !errors.Is(err, echo.ErrStatusRequestEntityTooLarge) {
				return handleError(c, core.NewInvalidRequestError("invalid "+encoding+" request body: "+err.Error(), err))
			}
			if err != nil || int64(len(body)) > limit {
				return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusRequestEntityTooLarge,
					"request body exceeds the body size limit", nil))
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.Header.Del("Content-Encoding")
			req.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return next(c)
		}
	}
}
