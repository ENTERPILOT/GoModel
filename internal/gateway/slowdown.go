package gateway

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func workflowSlowdown(workflow *core.Workflow) float64 {
	if workflow == nil || workflow.Resolution == nil || workflow.Resolution.Slowdown <= 0 {
		return 0
	}
	return workflow.Resolution.Slowdown
}

func waitForInferenceSlowdown(ctx context.Context, workflow *core.Workflow, inferenceTime time.Duration) error {
	factor := workflowSlowdown(workflow)
	if factor <= 0 || inferenceTime <= 0 {
		return nil
	}
	delay := time.Duration(float64(inferenceTime) * factor)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func executeResultWithSlowdown[Result any](
	ctx context.Context,
	workflow *core.Workflow,
	execute func() (Result, error),
) (Result, error) {
	started := time.Now()
	result, err := execute()
	if err != nil {
		var zero Result
		return zero, err
	}
	if err := waitForInferenceSlowdown(ctx, workflow, time.Since(started)); err != nil {
		var zero Result
		return zero, err
	}
	return result, nil
}

func dispatchTranslatedWithSlowdown[Response any](
	ctx context.Context,
	workflow *core.Workflow,
	execute func() (Response, ExecutionMeta, error),
) (Response, ExecutionMeta, error) {
	started := time.Now()
	resp, meta, err := execute()
	if err != nil {
		var zero Response
		return zero, ExecutionMeta{}, err
	}
	if err := waitForInferenceSlowdown(ctx, workflow, time.Since(started)); err != nil {
		var zero Response
		return zero, ExecutionMeta{}, err
	}
	return resp, meta, nil
}
