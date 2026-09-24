package usage

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingFlushListener records the order of flush notifications and
// whether the store write happened between them.
type recordingFlushListener struct {
	store  *mockStore
	events []string
}

func (l *recordingFlushListener) UsageFlushStarted() {
	l.events = append(l.events, "started", "entries="+strconv.Itoa(len(l.store.getEntries())))
}

func (l *recordingFlushListener) UsageFlushFinished() {
	l.events = append(l.events, "finished", "entries="+strconv.Itoa(len(l.store.getEntries())))
}

func TestLoggerFlushListenerWrapsBatchWrite(t *testing.T) {
	tests := []struct {
		name     string
		writeErr error
		want     []string
	}{
		{name: "written batch", want: []string{"started", "entries=0", "finished", "entries=1"}},
		{name: "failed batch still finishes", writeErr: errors.New("db down"), want: []string{"started", "entries=0", "finished", "entries=0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{writeErr: tt.writeErr}
			logger := NewLogger(store, Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
			listener := &recordingFlushListener{store: store}
			logger.SetFlushListener(listener)

			logger.Write(&UsageEntry{ID: "e1", RequestID: "r1"})
			require.NoError(t, logger.Close())
			assert.Equal(t, tt.want, listener.events)
		})
	}
}
