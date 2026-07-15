package responsecache

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

var (
	cacheLFEventBoundary   = []byte("\n\n")
	cacheCRLFEventBoundary = []byte("\r\n\r\n")
	cacheDataPrefix        = []byte("data:")
	cacheDonePayload       = []byte("[DONE]")
)

func cacheKeyRequestBody(path string, body []byte) []byte {
	switch path {
	case "/v1/chat/completions":
		req, err := core.DecodeChatRequest(body, nil)
		if err != nil || req == nil {
			return body
		}
		if req.Stream {
			req.StreamOptions = normalizeStreamOptionsForCache(req.StreamOptions)
		} else {
			req.StreamOptions = nil
		}
		normalized, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return normalized
	case "/v1/responses":
		req, err := core.DecodeResponsesRequest(body, nil)
		if err != nil || req == nil {
			return body
		}
		if req.Stream {
			req.StreamOptions = normalizeStreamOptionsForCache(req.StreamOptions)
		} else {
			req.StreamOptions = nil
		}
		normalized, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return normalized
	default:
		return body
	}
}

func isEventStreamContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "text/event-stream"
}

func writeCachedResponse(c *echo.Context, path string, requestBody, cached []byte, cacheType string) error {
	cacheHeader := cacheHeaderValue(cacheType)
	if isStreamingRequest(path, requestBody) {
		auditlog.EnrichEntryWithStream(c, true)
		auditlog.EnrichEntryWithCachedStreamResponse(c, path, cached)
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().Header().Set("Connection", "keep-alive")
		c.Response().Header().Set("X-Cache", cacheHeader)
		c.Response().WriteHeader(http.StatusOK)
		_, _ = c.Response().Write(cached)
		return nil
	}

	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Set("X-Cache", cacheHeader)
	c.Response().WriteHeader(http.StatusOK)
	_, _ = c.Response().Write(cached)
	return nil
}

func cacheHeaderValue(cacheType string) string {
	switch cacheType {
	case CacheTypeExact:
		return CacheHeaderExact
	case CacheTypeSemantic:
		return CacheHeaderSemantic
	default:
		return "HIT (" + cacheType + ")"
	}
}

func nextCacheEventBoundary(data []byte) (idx int, sepLen int) {
	lfIdx := bytes.Index(data, cacheLFEventBoundary)
	crlfIdx := bytes.Index(data, cacheCRLFEventBoundary)

	switch {
	case lfIdx == -1:
		if crlfIdx == -1 {
			return -1, 0
		}
		return crlfIdx, len(cacheCRLFEventBoundary)
	case crlfIdx == -1 || lfIdx < crlfIdx:
		return lfIdx, len(cacheLFEventBoundary)
	default:
		return crlfIdx, len(cacheCRLFEventBoundary)
	}
}

func parseCacheDataLine(line []byte) ([]byte, bool) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if !bytes.HasPrefix(line, cacheDataPrefix) {
		return nil, false
	}
	payload := bytes.TrimPrefix(line, cacheDataPrefix)
	if len(payload) > 0 && payload[0] == ' ' {
		payload = payload[1:]
	}
	return payload, true
}

func normalizeStreamOptionsForCache(src *core.StreamOptions) *core.StreamOptions {
	if src == nil || !src.IncludeUsage {
		return nil
	}
	cloned := *src
	return &cloned
}
