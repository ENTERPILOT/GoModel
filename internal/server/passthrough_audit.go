package server

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/auditlog"
)

// PassthroughAuditEntry carries the metadata captured for a single passthrough
// request. It intentionally omits request and response bodies — passthrough
// audit is about who called what and with what outcome, not about payload
// content. Best-effort model detection from the request body is included when
// the body was parseable.
type PassthroughAuditEntry struct {
	InstanceName string    // YAML key (e.g. "anthropic1")
	ProviderType string    // provider type (e.g. "anthropic")
	Method       string    // HTTP method
	Path         string    // original /p/{instance}/... request path
	Endpoint     string    // provider-relative endpoint forwarded upstream
	RequestID    string    // X-GoModel-Request-ID value for this request
	StatusCode   int       // upstream HTTP status code
	Timestamp    time.Time // when the request started
	Model        string    // best-effort model extracted from the request body (may be empty)
	ClientIP     string    // client IP address
}

// recordPassthroughAudit writes a lean audit log entry for a passthrough
// request. It is called synchronously at the end of ProviderPassthrough; the
// logger's Write implementation is non-blocking (async buffered).
func recordPassthroughAudit(logger auditlog.LoggerInterface, entry PassthroughAuditEntry) {
	if logger == nil || !logger.Config().Enabled {
		return
	}

	logEntry := &auditlog.LogEntry{
		ID:            uuid.NewString(),
		Timestamp:     entry.Timestamp,
		RequestID:     strings.TrimSpace(entry.RequestID),
		ClientIP:      strings.TrimSpace(entry.ClientIP),
		Method:        strings.TrimSpace(entry.Method),
		Path:          strings.TrimSpace(entry.Path),
		Provider:      strings.TrimSpace(entry.ProviderType),
		ProviderName:  strings.TrimSpace(entry.InstanceName),
		RequestedModel: strings.TrimSpace(entry.Model),
		StatusCode:    entry.StatusCode,
	}

	logger.Write(logEntry)
}
