package auditlog

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
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
	for path, want := range map[string]bool{
		"/v1/embeddings":          true,
		"/v1/embeddings/":         true,
		"/p/openai/v1/embeddings": true,
		"/p/openai/embeddings":    true,
		"/v1/chat/completions":    false,
		"/p/openai/v1/responses":  false,
		"/health":                 false,
	} {
		require.Equal(t, want, isEmbeddingsPath(path), path)
	}
}

func TestConfigForPath(t *testing.T) {
	off := Config{LogBodies: true, LogEmbeddingBodies: false}
	require.False(t, off.forPath("/v1/embeddings").LogBodies)
	require.True(t, off.forPath("/v1/chat/completions").LogBodies)

	on := Config{LogBodies: true, LogEmbeddingBodies: true}
	require.True(t, on.forPath("/v1/embeddings").LogBodies)

	master := Config{LogBodies: false, LogEmbeddingBodies: true}
	require.False(t, master.forPath("/v1/embeddings").LogBodies, "LogBodies stays the master switch")
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
		{"chat unaffected", "/v1/chat/completions", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &entryCapturingLogger{captureLiveLogger: captureLiveLogger{cfg: Config{
				Enabled: true, LogBodies: true, LogEmbeddingBodies: tc.embeddingBody,
			}}}
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
				require.NotNil(t, reqBody)
				require.NotNil(t, respBody)
				return
			}
			require.Nil(t, reqBody)
			require.Nil(t, respBody)
		})
	}
}
