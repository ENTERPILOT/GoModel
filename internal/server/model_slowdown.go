package server

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

func resolveModelSlowdown(
	ctx context.Context,
	resolver RequestModelResolver,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
) float64 {
	if slowdownResolver, ok := resolver.(gateway.ModelSlowdownResolver); ok {
		return slowdownResolver.ResolveSlowdown(ctx, requested, resolved)
	}
	return 0
}

func waitForModelSlowdownFactor(ctx context.Context, factor float64, inferenceTime time.Duration) error {
	if factor <= 0 || inferenceTime <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(float64(inferenceTime) * factor))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
