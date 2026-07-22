package conversationstore

import (
	"context"
	"errors"
	"testing"
)

func TestWaitForMongoMutationRetryHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForMongoMutationRetry(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context canceled", err)
	}
}
