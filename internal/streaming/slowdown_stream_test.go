package streaming

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowdownStreamDrainsUpstreamBeforeDelayedRelease(t *testing.T) {
	source := &timedChunkSource{}
	started := time.Now()
	stream := NewSlowdownStream(context.Background(), source, 5, started)
	t.Cleanup(func() { _ = stream.Close() })

	result := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(stream)
		result <- string(body)
	}()

	// The first source chunk arrives around 20ms and is due around 120ms with
	// factor 5. The background drainer must still consume the whole source while
	// the downstream reader is waiting, proving that delayed data is buffered.
	time.Sleep(60 * time.Millisecond)
	if reads := source.reads.Load(); reads < 3 {
		t.Fatalf("source reads after 60ms = %d, want at least 3 (fully drained)", reads)
	}

	select {
	case body := <-result:
		if body != "ab" {
			t.Fatalf("delayed body = %q, want %q", body, "ab")
		}
		if elapsed := time.Since(started); elapsed < 90*time.Millisecond {
			t.Fatalf("stream completed after %v, want scaled release near 120ms", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for delayed stream")
	}
}

func TestSlowdownStreamReadStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &timedChunkSource{}
	stream := NewSlowdownStream(ctx, source, 10, time.Now())
	t.Cleanup(func() { _ = stream.Close() })

	cancel()
	buf := make([]byte, 8)
	if _, err := stream.Read(buf); err != context.Canceled {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
}

func TestSlowdownStreamPreservesTerminalError(t *testing.T) {
	sourceErr := errors.New("upstream failed")
	tests := []struct {
		name       string
		source     io.ReadCloser
		firstChunk string
		wantErr    error
	}{
		{name: "EOF", source: io.NopCloser(strings.NewReader("x")), firstChunk: "x", wantErr: io.EOF},
		{name: "source error", source: io.NopCloser(terminalErrorReader{err: sourceErr}), wantErr: sourceErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := NewSlowdownStream(context.Background(), tt.source, 0.1, time.Now().Add(-time.Second))
			t.Cleanup(func() { _ = stream.Close() })
			buf := make([]byte, 8)
			if tt.firstChunk != "" {
				n, err := stream.Read(buf)
				if err != nil || string(buf[:n]) != tt.firstChunk {
					t.Fatalf("first Read() = (%q, %v), want (%q, nil)", buf[:n], err, tt.firstChunk)
				}
			}

			for read := 1; read <= 2; read++ {
				result := make(chan error, 1)
				go func() {
					_, err := stream.Read(buf)
					result <- err
				}()
				select {
				case err := <-result:
					if !errors.Is(err, tt.wantErr) {
						t.Fatalf("terminal Read() %d error = %v, want %v", read, err, tt.wantErr)
					}
				case <-time.After(time.Second):
					t.Fatalf("terminal Read() %d blocked", read)
				}
			}
		})
	}
}

func TestSlowdownStreamCancellationClosesBlockedUpstream(t *testing.T) {
	tests := []struct {
		name     string
		shutdown func(context.CancelFunc, io.Closer)
	}{
		{name: "parent context cancellation", shutdown: func(cancel context.CancelFunc, _ io.Closer) { cancel() }},
		{name: "explicit stream close", shutdown: func(_ context.CancelFunc, stream io.Closer) { _ = stream.Close() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			source := newBlockingCloseSource()
			stream := NewSlowdownStream(ctx, source, 1, time.Now())
			t.Cleanup(func() {
				cancel()
				_ = stream.Close()
			})

			select {
			case <-source.readStarted:
			case <-time.After(time.Second):
				t.Fatal("upstream Read did not start")
			}
			tt.shutdown(cancel, stream)

			select {
			case <-source.closed:
			case <-time.After(time.Second):
				t.Fatal("upstream Close was not called after shutdown")
			}
		})
	}
}

type timedChunkSource struct {
	reads atomic.Int32
}

func (s *timedChunkSource) Read(p []byte) (int, error) {
	switch s.reads.Add(1) {
	case 1:
		time.Sleep(20 * time.Millisecond)
		p[0] = 'a'
		return 1, nil
	case 2:
		p[0] = 'b'
		return 1, nil
	default:
		return 0, io.EOF
	}
}

func (*timedChunkSource) Close() error { return nil }

type blockingCloseSource struct {
	readStarted chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func newBlockingCloseSource() *blockingCloseSource {
	return &blockingCloseSource{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
	}
}

func (s *blockingCloseSource) Read([]byte) (int, error) {
	s.readOnce.Do(func() { close(s.readStarted) })
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingCloseSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type terminalErrorReader struct {
	err error
}

func (r terminalErrorReader) Read([]byte) (int, error) { return 0, r.err }
