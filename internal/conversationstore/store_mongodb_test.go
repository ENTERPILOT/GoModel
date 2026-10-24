package conversationstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaitForMongoMutationRetryHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForMongoMutationRetry(ctx, 0)
	require.ErrorIs(t, err, context.Canceled)
}
