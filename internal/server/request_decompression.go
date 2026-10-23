package server

import (
	"bytes"
	"compress/gzip"
	"context"
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

// requestDecodeAhead is how much of a compressed body is decoded before
// authentication: enough for the request snapshot and the selector peek.
const requestDecodeAhead = requestSnapshotInlineBodyLimit

// pendingRequestDecodeKey marks a request whose compressed body was decoded
// only in part; its value is the Content-Encoding.
type pendingRequestDecodeKey struct{}

// requestDecompression decodes request bodies sent with Content-Encoding gzip
// (or x-gzip) or zstd, so every handler, the audit capture, and passthrough
// see the JSON the client meant. The TypeSafe SDKs compress bodies of 1 KiB
// or more with zstd by default. The decoded body is held to limit as well
// (413): the body limit middleware only sees the compressed bytes, and a
// small compressed body can expand without bound. Other encodings, or more
// than one layer, are rejected with 415.
//
// Only the first requestDecodeAhead bytes are decoded here. A longer body is
// finished by finishRequestDecompression once the caller has authenticated,
// so a request without credentials cannot make the gateway expand a few
// hundred bytes into the whole body limit.
func requestDecompression(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			encoding, layers := requestContentEncoding(req.Header)
			if layers > 1 {
				return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusUnsupportedMediaType,
					"request bodies may use one Content-Encoding layer; send gzip, zstd, or an uncompressed body", nil))
			}
			if encoding == "" || encoding == "identity" || req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}
			var decoded io.ReadCloser
			switch encoding {
			case "gzip", "x-gzip":
				// The gzip header is read here, so an oversized body can
				// already fail the body limit.
				reader, err := gzip.NewReader(req.Body)
				if errors.Is(err, echo.ErrStatusRequestEntityTooLarge) {
					return handleError(c, requestBodyTooLarge())
				}
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
			body := &decodedRequestBody{decoded: decoded, compressed: req.Body}
			defer body.Close()

			ahead := min(limit, requestDecodeAhead)
			head, err := io.ReadAll(io.LimitReader(decoded, ahead+1))
			if err != nil {
				return handleError(c, requestDecodeError(encoding, err))
			}
			if int64(len(head)) <= ahead {
				_ = body.Close()
				setDecodedRequestBody(req, head)
				return next(c)
			}
			if ahead == limit {
				return handleError(c, requestBodyTooLarge())
			}
			body.Reader = io.MultiReader(bytes.NewReader(head), io.LimitReader(decoded, limit+1-int64(len(head))))
			req.Body = body
			req.ContentLength = -1
			req.Header.Del("Content-Encoding")
			req.Header.Del("Content-Length")
			c.SetRequest(req.WithContext(context.WithValue(req.Context(), pendingRequestDecodeKey{}, encoding)))
			return next(c)
		}
	}
}

// finishRequestDecompression decodes the rest of a body requestDecompression
// decoded only in part. It runs after authentication, and leaves the body
// complete and of known length, as handlers, session detection, and
// passthrough expect; an oversized or corrupt body is reported with its own
// status before they run.
func finishRequestDecompression(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			encoding, pending := req.Context().Value(pendingRequestDecodeKey{}).(string)
			if !pending {
				return next(c)
			}
			body, err := io.ReadAll(req.Body)
			// Release the decoder's window now rather than when the request
			// ends, which for a streamed response can be minutes away.
			_ = req.Body.Close()
			if err != nil {
				return handleError(c, requestDecodeError(encoding, err))
			}
			if int64(len(body)) > limit {
				return handleError(c, requestBodyTooLarge())
			}
			setDecodedRequestBody(req, body)
			// The snapshot saw a body of unknown length; record one too large
			// to capture as it would have been recorded with its length known.
			if snapshot := core.GetRequestSnapshot(req.Context()); snapshot != nil && !snapshot.BodyNotCaptured &&
				snapshotBodyNotCaptured(req, core.DescribeEndpoint(req.Method, req.URL.Path).BodyMode) {
				markSessionBodyNotCaptured(c, snapshot)
			}
			return next(c)
		}
	}
}

// decodedRequestBody reads a compressed body's decoded bytes and closes the
// decoder along with the body it reads.
type decodedRequestBody struct {
	io.Reader
	decoded    io.Closer
	compressed io.Closer
	closed     bool
}

func (b *decodedRequestBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.decoded.Close()
	return b.compressed.Close()
}

func setDecodedRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Del("Content-Encoding")
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func requestDecodeError(encoding string, err error) error {
	if errors.Is(err, echo.ErrStatusRequestEntityTooLarge) {
		return requestBodyTooLarge()
	}
	return core.NewInvalidRequestError("invalid "+encoding+" request body: "+err.Error(), err)
}

// requestContentEncoding returns the request's lowercased Content-Encoding
// and how many encoding layers it names. Every header field counts, not only
// the first, so "gzip" in one field and "identity" in another is two layers.
func requestContentEncoding(header http.Header) (string, int) {
	var encodings []string
	for _, value := range header.Values("Content-Encoding") {
		for encoding := range strings.SplitSeq(value, ",") {
			if encoding = strings.ToLower(strings.TrimSpace(encoding)); encoding != "" {
				encodings = append(encodings, encoding)
			}
		}
	}
	if len(encodings) == 0 {
		return "", 0
	}
	return encodings[0], len(encodings)
}

func requestBodyTooLarge() error {
	return core.NewInvalidRequestErrorWithStatus(http.StatusRequestEntityTooLarge, "request body exceeds the body size limit", nil)
}
