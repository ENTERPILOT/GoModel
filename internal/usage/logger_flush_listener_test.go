package usage

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoggerFlushListener(t *testing.T) {
	tests := []struct {
		name      string
		writeErr  error
		wantCalls int32
	}{
		{name: "runs after a written batch", wantCalls: 1},
		{name: "skipped when the batch fails", writeErr: errors.New("db down"), wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewLogger(&mockStore{writeErr: tt.writeErr}, Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
			var calls atomic.Int32
			logger.SetFlushListener(func() { calls.Add(1) })

			logger.Write(&UsageEntry{ID: "e1", RequestID: "r1"})
			require.NoError(t, logger.Close())
			assert.Equal(t, tt.wantCalls, calls.Load())
		})
	}
}
