package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

// The check runs before Echo's logger and audit middleware, so the warning
// handleError writes is the only trace a rejected request leaves.
func TestRequireClientCertificateLogsRejection(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	c, rec := echotest.Get(t, "/v1/models")
	next := func(*echo.Context) error { return c.String(http.StatusOK, "ok") }
	require.NoError(t, requireClientCertificate(next)(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	logOutput := buf.String()
	assert.Contains(t, logOutput, `"level":"WARN"`)
	assert.Contains(t, logOutput, `"code":"client_certificate_required"`)
	assert.Contains(t, logOutput, `"path":"/v1/models"`)
}
