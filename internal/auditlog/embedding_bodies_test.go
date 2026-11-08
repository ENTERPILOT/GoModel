package auditlog

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type entryCapturingLogger struct {
	captureLiveLogger
	written *LogEntry
}

func (l *entryCapturingLogger) Write(entry *LogEntry) { l.written = entry }

func TestIsEmbeddingsPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/v1/embeddings", true},
		{"/v1/embeddings/", true},
		{"/p/openai/v1/embeddings", true},
		{"/p/openai/embeddings", true},
		{"/p/azure/openai/deployments/emb/embeddings", true},
		{"/p/cohere/v2/embed", true},
		{"/p/ollama/api/embed", true},
		{"/p/gemini/v1beta/models/text-embedding-004:embedContent", true},
		{"/p/gemini/v1beta/models/text-embedding-004:batchEmbedContents", true},
		{"/v1/chat/completions", false},
		{"/p/openai/v1/responses", false},
		{"/p/cohere/v2/chat", false},
		{"/p/gemini/v1beta/models/gemini-2.5-flash:generateContent", false},
		{"/p/openai", false},
		{"/health", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, isEmbeddingsPath(tc.path))
		})
	}
}

func TestConfigForPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		path string
		want bool
	}{
		{"embeddings off drops bodies", Config{LogBodies: true}, "/v1/embeddings", false},
		{"embeddings off keeps chat", Config{LogBodies: true}, "/v1/chat/completions", true},
		{"embeddings on keeps bodies", Config{LogBodies: true, LogEmbeddingBodies: true}, "/v1/embeddings", true},
		{"log bodies stays master switch", Config{LogEmbeddingBodies: true}, "/v1/embeddings", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.ForPath(tc.path).LogBodies)
		})
	}
}

func TestMiddlewareEmbeddingBodies(t *testing.T) {
	for _, tc := range []struct {
		name           string
		path           string
		embeddingBody  bool
		wantBodiesKept bool
	}{
		{"embeddings off", "/v1/embeddings", false, false},
		{"embeddings on", "/v1/embeddings", true, true},
		{"passthrough embeddings off", "/p/openai/v1/embeddings", false, false},
		{"chat unaffected", "/v1/chat/completions", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &entryCapturingLogger{cfg: Config{
				Enabled: true, LogBodies: true, LogEmbeddingBodies: tc.embeddingBody,
			}}
			c, _ := echotest.Post(t, tc.path, nil)
			c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
				http.MethodPost, tc.path, nil, nil, nil, "application/json",
				[]byte(`{"model":"m","input":"secret"}`), false, "req-1", nil,
			))))
			err := Middleware(logger)(func(c *echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"answer": "x"})
			})(c)
			require.NoError(t, err)
			require.NotNil(t, logger.written, "metadata entry is still written")
			var reqBody, respBody any
			if logger.written.Data != nil {
				reqBody, respBody = logger.written.Data.RequestBody, logger.written.Data.ResponseBody
			}
			if tc.wantBodiesKept {
				assert.NotNil(t, reqBody)
				assert.NotNil(t, respBody)
				return
			}
			assert.Nil(t, reqBody)
			assert.Nil(t, respBody)
		})
	}
}

func TestEnrichEntryWithAttemptsEmbeddingBodies(t *testing.T) {
	for _, tc := range []struct {
		name          string
		path          string
		embeddingBody bool
		wantBodyKept  bool
	}{
		{"embeddings off", "/v1/embeddings", false, false},
		{"embeddings on", "/v1/embeddings", true, true},
		{"chat unaffected", "/v1/chat/completions", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &captureLiveLogger{cfg: Config{
				Enabled: true, LogBodies: true, LogHeaders: true, LogEmbeddingBodies: tc.embeddingBody,
			}}
			entry := &LogEntry{}
			c, _ := echotest.Post(t, tc.path, nil)
			c.Set(string(LogEntryKey), entry)
			c.Set(string(LogEntryLivePublisherKey), logger)

			EnrichEntryWithAttempts(c, []AttemptSnapshot{{
				Seq: 1, Kind: AttemptKindPrimary, StatusCode: http.StatusBadRequest,
				ResponseBody:    map[string]any{"error": "input echoed: secret"},
				ResponseHeaders: map[string]string{"X-Request-Id": "r1"},
			}})

			require.NotNil(t, entry.Data)
			require.Len(t, entry.Data.Attempts, 1)
			attempt := entry.Data.Attempts[0]
			assert.Equal(t, tc.wantBodyKept, attempt.ResponseBody != nil)
			assert.NotNil(t, attempt.ResponseHeaders, "headers follow LogHeaders only")
		})
	}
}

func TestStreamLogObserverEmbeddingBodies(t *testing.T) {
	logger := &captureLiveLogger{cfg: Config{Enabled: true, LogBodies: true}}

	assert.False(t, NewStreamLogObserver(logger, &LogEntry{}, "/p/openai/v1/embeddings").WantsJSONEvent(nil))
	assert.True(t, NewStreamLogObserver(logger, &LogEntry{}, "/v1/chat/completions").WantsJSONEvent(nil))
}
