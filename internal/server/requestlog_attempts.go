package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

func enrichRequestLogEntryWithProviderAttempts(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	// Without a request log entry (request logging disabled) the snapshot conversion
	// below — including per-attempt body capture — would be discarded anyway.
	if requestlog.GetStreamEntryFromContext(c) == nil {
		return
	}
	attempts := requestLogAttemptsFromGateway(c.Request().Context())
	if len(attempts) == 0 {
		return
	}
	requestlog.EnrichEntryWithAttempts(c, attempts)
}

func requestLogAttemptsFromGateway(ctx context.Context) []requestlog.AttemptSnapshot {
	gatewayAttempts := gateway.AttemptsFromContext(ctx)
	if len(gatewayAttempts) == 0 {
		return nil
	}
	attempts := make([]requestlog.AttemptSnapshot, 0, len(gatewayAttempts))
	for _, attempt := range gatewayAttempts {
		attempts = append(attempts, requestlog.AttemptSnapshot{
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
			ResponseBody:    requestlog.CaptureAttemptResponseBody(attempt.ResponseBody),
			ResponseHeaders: requestlog.RedactAttemptResponseHeaders(attempt.ResponseHeaders),

			UpstreamErrorMessage: attempt.UpstreamErrorMessage,
		})
	}
	return attempts
}
