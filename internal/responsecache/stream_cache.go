package responsecache

import (
	"bytes"
	"maps"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

var (
	cacheLFEventBoundary   = []byte("\n\n")
	cacheCRLFEventBoundary = []byte("\r\n\r\n")
	cacheDataPrefix        = []byte("data:")
	cacheDonePayload       = []byte("[DONE]")
)

type streamResponseDefaults struct {
	Model    string
	Provider string
}

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

func reconstructStreamingResponse(path string, raw []byte, defaults streamResponseDefaults) ([]byte, bool) {
	switch path {
	case "/v1/chat/completions":
		builder := &chatStreamCacheBuilder{
			defaults: defaults,
			Choices:  make(map[int]*chatChoiceState),
		}
		parseSSEJSONEvents(raw, builder.OnJSONEvent)
		return builder.Build()
	case "/v1/responses":
		builder := &responsesStreamCacheBuilder{
			defaults: defaults,
			Output:   make(map[int]*responsesOutputState),
			ItemIDs:  make(map[string]int),
		}
		parseSSEJSONEvents(raw, builder.OnJSONEvent)
		return builder.Build()
	default:
		return nil, false
	}
}

func parseSSEJSONEvents(raw []byte, onJSON func(map[string]any)) {
	for len(raw) > 0 {
		idx, sepLen := nextCacheEventBoundary(raw)
		event := raw
		if idx != -1 {
			event = raw[:idx]
			raw = raw[idx+sepLen:]
		} else {
			raw = nil
		}

		payload, ok := parseCacheEventJSON(event)
		if !ok {
			if idx == -1 {
				break
			}
			continue
		}
		onJSON(payload)
		if idx == -1 {
			break
		}
	}
}

func parseCacheEventJSON(event []byte) (map[string]any, bool) {
	lines := bytes.Split(event, []byte("\n"))
	payloadLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		data, ok := parseCacheDataLine(line)
		if !ok {
			continue
		}
		payloadLines = append(payloadLines, data)
	}
	if len(payloadLines) == 0 {
		return nil, false
	}

	jsonData := bytes.Join(payloadLines, []byte("\n"))
	if bytes.Equal(jsonData, cacheDonePayload) {
		return nil, false
	}

	var payload map[string]any
	if err := json.Unmarshal(jsonData, &payload); err != nil {
		return nil, false
	}
	return payload, true
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

func streamIncludeUsageRequested(path string, requestBody []byte) bool {
	switch path {
	case "/v1/chat/completions":
		req, err := core.DecodeChatRequest(requestBody, nil)
		if err != nil || req == nil {
			return false
		}
		return normalizeStreamOptionsForCache(req.StreamOptions) != nil
	case "/v1/responses":
		req, err := core.DecodeResponsesRequest(requestBody, nil)
		if err != nil || req == nil {
			return false
		}
		return normalizeStreamOptionsForCache(req.StreamOptions) != nil
	default:
		return false
	}
}

func appendSSEJSONEvent(out *bytes.Buffer, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if eventName != "" {
		out.WriteString("event: ")
		out.WriteString(eventName)
		out.WriteByte('\n')
	}
	out.WriteString("data: ")
	out.Write(data)
	out.WriteString("\n\n")
	return nil
}

func toJSONMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloneJSONMap(src map[string]any) map[string]any {
	return maps.Clone(src)
}

func jsonNumberToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func jsonNumberToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
