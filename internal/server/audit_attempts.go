package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/gateway"
)

func enrichAuditEntryWithProviderAttempts(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	// Without an audit entry (audit logging disabled) the snapshot conversion
	// below — including per-attempt body capture — would be discarded anyway.
	if auditlog.GetStreamEntryFromContext(c) == nil {
		return
	}
	attempts := auditAttemptsFromGateway(c.Request().Context())
	if len(attempts) == 0 {
		return
	}
	auditlog.EnrichEntryWithAttempts(c, attempts)
}

func auditAttemptsFromGateway(ctx context.Context) []auditlog.AttemptSnapshot {
	gatewayAttempts := gateway.AttemptsFromContext(ctx)
	if len(gatewayAttempts) == 0 {
		return nil
	}
	attempts := make([]auditlog.AttemptSnapshot, 0, len(gatewayAttempts))
	for _, attempt := range gatewayAttempts {
		attempts = append(attempts, auditlog.AttemptSnapshot{
			Seq:             attempt.Seq,
			Kind:            attempt.Kind,
			ProviderType:    attempt.ProviderType,
			ProviderName:    attempt.ProviderName,
			Model:           attempt.Model,
			StatusCode:      attempt.StatusCode,
			Success:         attempt.Success,
			ErrorType:       attempt.ErrorType,
			ErrorCode:       attempt.ErrorCode,
			ErrorMessage:    attempt.ErrorMessage,
			StartedAt:       attempt.StartedAt,
			DurationNs:      attempt.DurationNs,
			ResponseBody:    auditlog.CaptureAttemptResponseBody(attempt.ResponseBody),
			ResponseHeaders: auditlog.RedactAttemptResponseHeaders(attempt.ResponseHeaders),
		})
	}
	return attempts
}
