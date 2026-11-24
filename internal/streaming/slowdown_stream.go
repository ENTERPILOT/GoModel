package streaming

import (
	"context"
	"io"
	"sync"
	"time"
)

// NewSlowdownStream drains source in the background and releases each read
// chunk on a scaled timeline. factor is extra time: 0.5 makes a chunk that
// arrived 2s after inference start visible at 3s. Draining independently lets
// the upstream continue producing while delayed chunks accumulate in memory.
func NewSlowdownStream(ctx context.Context, source io.ReadCloser, factor float64, inferenceStarted time.Time) io.ReadCloser {
	return NewSlowdownStreamWithState(ctx, source, factor, inferenceStarted, nil)
}

// DeliveryState is state the reader of a stream depends on that changes as
// the stream is read: the cost of the events read so far, which the response
// writes beside them. A slowdown stream reads ahead of what it delivers, so
// it captures the state after each chunk it reads and restores it when that
// chunk is delivered: the reader sees the state as of the bytes it holds.
type DeliveryState interface {
	// Capture returns the state as it stands.
	Capture() any
	// Restore makes a captured state the one the reader sees.
	Restore(state any)
}

// NewSlowdownStreamWithState is NewSlowdownStream for a stream whose reader
// depends on state; nil is none.
func NewSlowdownStreamWithState(ctx context.Context, source io.ReadCloser, factor float64, inferenceStarted time.Time, state DeliveryState) io.ReadCloser {
	if source == nil || factor <= 0 {
		return source
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if inferenceStarted.IsZero() {
		inferenceStarted = time.Now()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	s := &slowdownStream{
		source:    source,
		state:     state,
		factor:    factor,
		started:   inferenceStarted,
		ctx:       streamCtx,
		cancel:    cancel,
		notify:    make(chan struct{}, 1),
		drainDone: make(chan struct{}),
	}
	go s.drain()
	go s.closeSourceOnCancellation()
	return s
}

type slowdownChunk struct {
	data  []byte
	due   time.Time
	err   error
	state any // the delivery state once the chunk was read
}

type slowdownStream struct {
	source    io.ReadCloser
	state     DeliveryState
	factor    float64
	started   time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	notify    chan struct{}
	drainDone chan struct{}

	mu              sync.Mutex
	queue           []slowdownChunk
	closed          bool
	terminalErr     error
	closeOnce       sync.Once
	sourceCloseOnce sync.Once
	sourceCloseErr  error
}

func (s *slowdownStream) drain() {
	defer close(s.drainDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.source.Read(buf)
		due := s.scaledDue(time.Since(s.started))
		var state any
		if s.state != nil {
			state = s.state.Capture()
		}
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if !s.enqueue(slowdownChunk{data: data, due: due, state: state}) {
				return
			}
		}
		if err != nil {
			s.enqueue(slowdownChunk{due: due, err: err, state: state})
			_ = s.closeSource()
			return
		}
	}
}

func (s *slowdownStream) closeSourceOnCancellation() {
	select {
	case <-s.ctx.Done():
		_ = s.closeSource()
	case <-s.drainDone:
	}
}

func (s *slowdownStream) closeSource() error {
	s.sourceCloseOnce.Do(func() {
		s.sourceCloseErr = s.source.Close()
	})
	return s.sourceCloseErr
}

func (s *slowdownStream) scaledDue(elapsed time.Duration) time.Time {
	scaled := time.Duration(float64(elapsed) * (1 + s.factor))
	return s.started.Add(scaled)
}

func (s *slowdownStream) enqueue(chunk slowdownChunk) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, chunk)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return true
}

func (s *slowdownStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		chunk, ok, err := s.peek()
		if err != nil {
			return 0, err
		}
		if !ok {
			select {
			case <-s.notify:
				continue
			case <-s.ctx.Done():
				return 0, s.ctx.Err()
			}
		}

		if wait := time.Until(chunk.due); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-s.ctx.Done():
				timer.Stop()
				return 0, s.ctx.Err()
			}
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, io.ErrClosedPipe
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			continue
		}
		front := &s.queue[0]
		if s.state != nil {
			s.state.Restore(front.state)
		}
		if len(front.data) > 0 {
			n := copy(p, front.data)
			front.data = front.data[n:]
			if len(front.data) == 0 {
				s.popLocked()
			}
			s.mu.Unlock()
			return n, nil
		}
		err = front.err
		if err == nil {
			err = io.EOF
		}
		s.terminalErr = err
		s.popLocked()
		s.mu.Unlock()
		return 0, err
	}
}

func (s *slowdownStream) peek() (slowdownChunk, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return slowdownChunk{}, false, io.ErrClosedPipe
	}
	if s.terminalErr != nil {
		return slowdownChunk{}, false, s.terminalErr
	}
	if len(s.queue) == 0 {
		return slowdownChunk{}, false, nil
	}
	return s.queue[0], true, nil
}

func (s *slowdownStream) popLocked() {
	s.queue[0] = slowdownChunk{}
	s.queue = s.queue[1:]
	if len(s.queue) == 0 {
		s.queue = nil
	}
}

func (s *slowdownStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		s.queue = nil
		s.mu.Unlock()
		_ = s.closeSource()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	})
	return s.sourceCloseErr
}
