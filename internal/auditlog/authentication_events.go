package auditlog

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

const authenticationEventProvider = "authentication"

type authenticationEventRecorder struct {
	logger LoggerInterface
}

// NewAuthenticationEventRecorder adapts extension authentication lifecycle
// events into ordinary durable audit entries. It intentionally uses the same
// logger, retention, storage backend, and shutdown flush as request auditing.
func NewAuthenticationEventRecorder(logger LoggerInterface) ext.AuthenticationEventRecorder {
	return &authenticationEventRecorder{logger: logger}
}

func (r *authenticationEventRecorder) RecordAuthenticationEvent(event ext.AuthenticationEvent) {
	if r == nil || r.logger == nil || !r.logger.Config().Enabled {
		return
	}

	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	userPath, err := core.NormalizeUserPath(event.UserPath)
	if err != nil || userPath == "" {
		userPath = "/"
	}
	method := NormalizeAuthMethod(event.Method)
	if method == "" {
		method = AuthMethodExtension
	}
	status := 200
	errorType := ""
	eventType := NormalizeAuthMethod(event.Type)
	if eventType == "" {
		eventType = "authentication"
	}
	reason := NormalizeAuthMethod(event.Reason)
	if strings.EqualFold(strings.TrimSpace(event.Outcome), "failure") {
		status = 401
		errorType = "authentication_error"
		if reason == "" {
			reason = "unspecified"
		}
	}

	r.logger.Write(&LogEntry{
		ID:          uuid.NewString(),
		Timestamp:   timestamp.UTC(),
		Provider:    authenticationEventProvider,
		StatusCode:  status,
		RequestID:   strings.TrimSpace(event.RequestID),
		PrincipalID: strings.TrimSpace(event.PrincipalID),
		AuthMethod:  method,
		ClientIP:    strings.TrimSpace(event.ClientIP),
		Method:      strings.TrimSpace(event.HTTPMethod),
		Path:        core.RedactSensitiveURLQuery(strings.TrimSpace(event.Path)),
		UserPath:    userPath,
		ErrorType:   errorType,
		Data: &LogData{
			UserAgent: strings.TrimSpace(event.UserAgent),
			EventType: eventType,
			ErrorCode: reason,
		},
	})
}
