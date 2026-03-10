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

func TestChatRequestFromSemanticEnvelope_CachesCanonicalRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-5-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hi"}],
			"response_format":{"type":"json_schema"}
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := chatRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	second, err := chatRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.NotNil(t, first.ExtraFields["response_format"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.ChatRequest)
	assert.Equal(t, "gpt-5-mini", env.SelectorHints.Model)
	assert.Equal(t, "openai", env.SelectorHints.Provider)
}

func TestResponsesRequestFromSemanticEnvelope_CachesCanonicalRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/responses",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-5-mini",
			"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := responsesRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	second, err := responsesRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)

	input, ok := first.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	require.NotNil(t, input[0].ExtraFields["x_trace"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.ResponsesRequest)
	assert.Equal(t, "gpt-5-mini", env.SelectorHints.Model)
}

func TestEmbeddingRequestFromSemanticEnvelope_FallsBackToLiveBodyWhenIngressBodyMissing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{
		"model":"text-embedding-3-large",
		"provider":"openai",
		"input":"hello",
		"x_meta":{"trace":"abc"}
	}`))
	req.Header.Set("Content-Type", "application/json")

	frame := &core.IngressFrame{
		Method:          http.MethodPost,
		Path:            "/v1/embeddings",
		ContentType:     "application/json",
		RawBodyTooLarge: true,
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	embeddingReq, err := embeddingRequestFromSemanticEnvelope(c)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", embeddingReq.Model)
	require.Equal(t, "openai", embeddingReq.Provider)
	require.NotNil(t, embeddingReq.ExtraFields["x_meta"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, embeddingReq, env.EmbeddingRequest)
	assert.True(t, env.JSONBodyParsed)
	assert.Equal(t, "text-embedding-3-large", env.SelectorHints.Model)
}

func TestBatchRequestFromSemanticEnvelope_CachesCanonicalRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/batches",
		ContentType: "application/json",
		RawBody: []byte(`{
			"completion_window":"24h",
			"requests":[{
				"custom_id":"chat-1",
				"url":"/v1/chat/completions",
				"body":{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]},
				"x_item_flag":{"enabled":true}
			}],
			"x_top":{"trace":"batch-1"}
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := batchRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	second, err := batchRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.NotNil(t, first.ExtraFields["x_top"])
	require.Len(t, first.Requests, 1)
	require.NotNil(t, first.Requests[0].ExtraFields["x_item_flag"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.BatchRequest)
	assert.True(t, env.JSONBodyParsed)
}

func TestBatchRequestMetadataFromSemanticEnvelope_CachesListMetadata(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/batches?after=batch_prev&limit=5", nil)
	frame := &core.IngressFrame{
		Method: http.MethodGet,
		Path:   "/v1/batches",
		QueryParams: map[string][]string{
			"after": {"batch_prev"},
			"limit": {"5"},
		},
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := batchRequestMetadataFromSemanticEnvelope(c)
	require.NoError(t, err)
	second, err := batchRequestMetadataFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.BatchActionList, first.Action)
	assert.Equal(t, "batch_prev", first.After)
	assert.True(t, first.HasLimit)
	assert.Equal(t, 5, first.Limit)

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.BatchMetadata)
}

func TestFileRequestFromSemanticEnvelope_InvalidLimitFromIngressReturnsError(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/files?limit=bad", nil)
	frame := &core.IngressFrame{
		Method: http.MethodGet,
		Path:   "/v1/files",
		QueryParams: map[string][]string{
			"limit": {"bad"},
		},
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	_, err := fileRequestFromSemanticEnvelope(c)
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

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/files",
		ContentType: writer.FormDataContentType(),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := fileRequestFromSemanticEnvelope(c)
	require.NoError(t, err)
	second, err := fileRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.FileActionCreate, first.Action)
	assert.Equal(t, "openai", first.Provider)
	assert.Equal(t, "batch", first.Purpose)
	assert.Equal(t, "requests.jsonl", first.Filename)

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.FileRequest)
	assert.Equal(t, "openai", env.SelectorHints.Provider)
}

func TestFileRequestFromSemanticEnvelope_CachesListMetadata(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/v1/files?provider=openai&purpose=batch&after=file_prev&limit=5", nil)
	frame := &core.IngressFrame{
		Method: http.MethodGet,
		Path:   "/v1/files",
		QueryParams: map[string][]string{
			"provider": {"openai"},
			"purpose":  {"batch"},
			"after":    {"file_prev"},
			"limit":    {"5"},
		},
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := fileRequestFromSemanticEnvelope(c)
	require.NoError(t, err)
	second, err := fileRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	assert.Equal(t, core.FileActionList, first.Action)
	assert.Equal(t, "openai", first.Provider)
	assert.Equal(t, "batch", first.Purpose)
	assert.Equal(t, "file_prev", first.After)
	assert.True(t, first.HasLimit)
	assert.Equal(t, 5, first.Limit)
}
