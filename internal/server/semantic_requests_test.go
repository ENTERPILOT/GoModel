package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestCanonicalJSONRequestFromSemanticEnvelope_CachesChatRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hi"}],
			"response_format":{"type":"json_schema"}
		}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := canonicalJSONRequestFromSemantics[*core.ChatRequest](c, core.DecodeChatRequest)
	require.NoError(t, err)

	second, err := canonicalJSONRequestFromSemantics[*core.ChatRequest](c, core.DecodeChatRequest)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.NotNil(t, first.ExtraFields.Lookup("response_format"))

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.CachedChatRequest())
	assert.Equal(t, "gpt-5-mini", env.RouteHints.Model)
	assert.Equal(t, "openai", env.RouteHints.Provider)
}

func TestCanonicalJSONRequestFromSemanticEnvelope_CachesResponsesRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/responses",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-mini",
			"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
		}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := canonicalJSONRequestFromSemantics[*core.ResponsesRequest](c, core.DecodeResponsesRequest)
	require.NoError(t, err)

	second, err := canonicalJSONRequestFromSemantics[*core.ResponsesRequest](c, core.DecodeResponsesRequest)
	require.NoError(t, err)

	require.Same(t, first, second)

	input, ok := first.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	require.NotNil(t, input[0].ExtraFields.Lookup("x_trace"))

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.CachedResponsesRequest())
	assert.Equal(t, "gpt-5-mini", env.RouteHints.Model)
}

func TestCanonicalJSONRequestFromSemanticEnvelope_FallsBackToLiveBodyWhenIngressBodyMissing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{
		"model":"text-embedding-3-large",
		"provider":"openai",
		"input":"hello",
		"x_meta":{"trace":"abc"}
	}`))
	req.Header.Set("Content-Type", "application/json")

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/embeddings", nil, nil, nil, "application/json", nil, true, "", nil)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	embeddingReq, err := canonicalJSONRequestFromSemantics[*core.EmbeddingRequest](c, core.DecodeEmbeddingRequest)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", embeddingReq.Model)
	require.Equal(t, "openai", embeddingReq.Provider)
	require.NotNil(t, embeddingReq.ExtraFields["x_meta"])

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, embeddingReq, env.CachedEmbeddingRequest())
	assert.True(t, env.JSONBodyParsed)
	assert.Equal(t, "text-embedding-3-large", env.RouteHints.Model)
}

func TestCanonicalJSONRequestFromSemanticEnvelope_CachesBatchRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/batches",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"completion_window":"24h",
			"requests":[{
				"custom_id":"chat-1",
				"url":"/v1/chat/completions",
				"body":{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]},
				"x_item_flag":{"enabled":true}
			}],
			"x_top":{"trace":"batch-1"}
		}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := canonicalJSONRequestFromSemantics[*core.BatchRequest](c, core.DecodeBatchRequest)
	require.NoError(t, err)

	second, err := canonicalJSONRequestFromSemantics[*core.BatchRequest](c, core.DecodeBatchRequest)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.NotNil(t, first.ExtraFields["x_top"])
	require.Len(t, first.Requests, 1)
	require.NotNil(t, first.Requests[0].ExtraFields["x_item_flag"])

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.CachedBatchRequest())
	assert.True(t, env.JSONBodyParsed)
}

func TestBatchRequestMetadataFromSemanticEnvelope_CachesListMetadata(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/batches?after=batch_prev&limit=5", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/batches",
		nil,
		map[string][]string{
			"after": {"batch_prev"},
			"limit": {"5"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := batchRouteInfoFromSemantics(c)
	require.NoError(t, err)
	second, err := batchRouteInfoFromSemantics(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.BatchActionList, first.Action)
	assert.Equal(t, "batch_prev", first.After)
	assert.True(t, first.HasLimit)
	assert.Equal(t, 5, first.Limit)

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.CachedBatchRouteInfo())
}

func TestFileRequestFromSemanticEnvelope_InvalidLimitFromIngressReturnsError(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/files?limit=bad", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/files",
		nil,
		map[string][]string{
			"limit": {"bad"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	_, err := fileRouteInfoFromSemantics(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid limit parameter")
}

func TestFileRequestFromSemanticEnvelope_EnrichesCreateMetadata(t *testing.T) {
	e := echo.New()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("provider", "openai"))
	require.NoError(t, writer.WriteField("purpose", "batch"))
	part, err := writer.CreateFormFile("file", "requests.jsonl")
	require.NoError(t, err)
	_, err = part.Write([]byte("{\"custom_id\":\"1\"}\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := fileRouteInfoFromSemantics(c)
	require.NoError(t, err)
	second, err := fileRouteInfoFromSemantics(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.FileActionCreate, first.Action)
	assert.Equal(t, "openai", first.Provider)
	assert.Equal(t, "batch", first.Purpose)
	assert.Equal(t, "requests.jsonl", first.Filename)

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.CachedFileRouteInfo())
	assert.Equal(t, "openai", env.RouteHints.Provider)
}

func TestFileRequestFromSemanticEnvelope_CachesListMetadata(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/files?provider=openai&purpose=batch&after=file_prev&limit=5", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/files",
		nil,
		map[string][]string{
			"provider": {"openai"},
			"purpose":  {"batch"},
			"after":    {"file_prev"},
			"limit":    {"5"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := fileRouteInfoFromSemantics(c)
	require.NoError(t, err)
	second, err := fileRouteInfoFromSemantics(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.FileActionList, first.Action)
	assert.Equal(t, "openai", first.Provider)
	assert.Equal(t, "batch", first.Purpose)
	assert.Equal(t, "file_prev", first.After)
	assert.True(t, first.HasLimit)
	assert.Equal(t, 5, first.Limit)
}
