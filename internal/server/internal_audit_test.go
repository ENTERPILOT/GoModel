package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// A gateway-internal call follows the body-logging switches for upstream
// error text like a client request does.
func TestFinishInternalAuditEntry_UpstreamErrorMessageFollowsBodyLogging(t *testing.T) {
	upstream := core.ParseProviderError("openai", http.StatusBadRequest, []byte(`{"error":{"message":"Invalid input: the user's prompt","code":"invalid_value"}}`), nil)
	gatewayErr := core.NewInvalidRequestError("model is required", nil)

	tests := []struct {
		name string
		path string
		cfg  auditlog.Config
		err  error
		want string
	}{
		{name: "bodies off", path: "/v1/chat/completions", cfg: auditlog.Config{Enabled: true}, err: upstream, want: auditlog.UpstreamErrorMessageWithheld},
		{name: "bodies on", path: "/v1/chat/completions", cfg: auditlog.Config{Enabled: true, LogBodies: true}, err: upstream, want: "Invalid input: the user's prompt"},
		{name: "embedding bodies off", path: "/v1/embeddings", cfg: auditlog.Config{Enabled: true, LogBodies: true}, err: upstream, want: auditlog.UpstreamErrorMessageWithheld},
		{name: "gateway message kept with bodies off", path: "/v1/chat/completions", cfg: auditlog.Config{Enabled: true}, err: gatewayErr, want: "model is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &capturingAuditLogger{config: tt.cfg}
			entry := &auditlog.LogEntry{Path: tt.path, Data: &auditlog.LogData{}}

			finishInternalAuditEntry(logger, context.Background(), entry, internalExchange{path: tt.path, start: time.Now(), err: tt.err})

			require.Len(t, logger.entries, 1)
			assert.Equal(t, tt.want, entry.Data.ErrorMessage)
			assert.Equal(t, string(core.ErrorTypeInvalidRequest), entry.ErrorType)
			assert.Equal(t, http.StatusBadRequest, entry.StatusCode)
		})
	}
}
