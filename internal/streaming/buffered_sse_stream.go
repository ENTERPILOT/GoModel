package streaming

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultBufferMaxBytes         = 4 * 1024 * 1024
	defaultBufferKeepAlive        = 15 * time.Second
	defaultBufferKeepAliveComment = ": gomodel-buffering"
	bufferReadSize                = 32 * 1024
)

// ErrBufferLimit reports that a buffered upstream exceeded MaxBytes.
var ErrBufferLimit = errors.New("streaming: buffered stream exceeded size limit")

// BufferOptions tunes NewBufferedSSEStream.
type BufferOptions struct {
	// MaxBytes caps the buffered upstream bytes; 0 selects 4 MiB. Exceeding
	// it fails closed with error code "response_too_large".
	MaxBytes int
	// KeepAliveInterval spaces the SSE comments sent to the client while the
	// upstream is being drained; 0 selects 15s, a negative value disables
	// them.
	KeepAliveInterval time.Duration
	// KeepAliveComment is the comment text; default ": gomodel-buffering".
	KeepAliveComment string
	// OnError receives the buffer limit and finisher errors behind a
	// fail-closed replay.
	OnError func(error)
}

// Finisher receives the whole upstream once drained: its decoded events
// (comments and oversized fragments excluded, [DONE] included) and its raw
// bytes. It returns the bytes to replay to the client; nil replays raw
// unchanged. An error fails closed with error code "plugin_failure".
type Finisher func(events []Event, raw []byte) (replay []byte, err error)

// NewBufferedSSEStream holds the whole upstream before anything reaches the
// client. The first Read starts draining upstream into a bounded buffer;
// while draining, Read blocks and returns a keep-alive comment whenever the
// interval elapses. Once upstream ends the finisher runs and its replay is
// served until EOF. Cancelling ctx (client gone) stops the drain, closes
// upstream and makes Read return ctx.Err() without running the finisher.
// When upstream fails mid-stream the finisher still runs on what arrived and
// the upstream error is returned once the replay has been served.
//
// Closing upstream must unblock a pending Read (net/http bodies do).
func NewBufferedSSEStream(ctx context.Context, upstream io.ReadCloser, codec Codec, finish Finisher, opts BufferOptions) io.ReadCloser {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultBufferMaxBytes
	}
	if opts.KeepAliveInterval == 0 {
		opts.KeepAliveInterval = defaultBufferKeepAlive
	}
	if opts.KeepAliveComment == "" {
		opts.KeepAliveComment = defaultBufferKeepAliveComment
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &bufferedSSEStream{
		ctx:       ctx,
		upstream:  upstream,
		codec:     codec,
		finish:    finish,
		opts:      opts,
		keepAlive: []byte(opts.KeepAliveComment + "\n\n"),
		done:      make(chan struct{}),
	}
}

type bufferedSSEStream struct {
	ctx       context.Context
	upstream  io.ReadCloser
	codec     Codec
	finish    Finisher
	opts      BufferOptions
	keepAlive []byte

	start     sync.Once
	done      chan struct{}
	ticker    *time.Ticker
	stopAfter func() bool

	// Written by the drain goroutine, read after done is closed.
	events   []Event
	raw      []byte
	drainErr error
	tooLarge bool

	pending   []byte
	replay    []byte
	replayPos int
	finalized bool
	finalErr  error

	closed        atomic.Bool
	upstreamClose sync.Once
	upstreamErr   error
}

func (s *bufferedSSEStream) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, ErrStreamClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	s.start.Do(s.startDrain)

	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	if !s.finalized {
		var tick <-chan time.Time
		if s.ticker != nil {
			tick = s.ticker.C
		}
		select {
		case <-s.done:
			s.finalize()
		case <-tick:
			s.pending = s.keepAlive
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			return n, nil
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		}
	}
	if s.replayPos < len(s.replay) {
		n := copy(p, s.replay[s.replayPos:])
		s.replayPos += n
		return n, nil
	}
	return 0, s.finalErr
}

func (s *bufferedSSEStream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.ticker != nil {
		s.ticker.Stop()
	}
	if s.stopAfter != nil {
		s.stopAfter()
	}
	return s.closeUpstream()
}

func (s *bufferedSSEStream) closeUpstream() error {
	s.upstreamClose.Do(func() { s.upstreamErr = s.upstream.Close() })
	return s.upstreamErr
}

func (s *bufferedSSEStream) startDrain() {
	if s.opts.KeepAliveInterval > 0 {
		s.ticker = time.NewTicker(s.opts.KeepAliveInterval)
	}
	s.stopAfter = context.AfterFunc(s.ctx, func() { _ = s.closeUpstream() })
	go s.drain()
}

func (s *bufferedSSEStream) drain() {
	defer close(s.done)
	scanner := EventScanner{MaxEventBytes: max(s.opts.MaxBytes, maxPendingEventBytes)}
	buf := make([]byte, bufferReadSize)
	for {
		n, err := s.upstream.Read(buf)
		if n > 0 && !s.ingest(scanner.Feed(buf[:n])) {
			return
		}
		if err != nil {
			if err != io.EOF {
				s.drainErr = err
			}
			s.ingest(scanner.Flush())
			return
		}
	}
}

// ingest buffers raw events and decodes them; it returns false once the
// buffer limit is exceeded.
func (s *bufferedSSEStream) ingest(raws []RawEvent) bool {
	for _, raw := range raws {
		if len(s.raw)+len(raw.Raw) > s.opts.MaxBytes {
			s.tooLarge = true
			_ = s.closeUpstream()
			return false
		}
		s.raw = append(s.raw, raw.Raw...)
		if raw.Comment || raw.Oversized {
			continue
		}
		ev := s.codec.Decode(raw, len(s.events))
		ev.Data = append([]byte(nil), ev.Data...)
		s.events = append(s.events, ev)
	}
	return true
}

// finalize runs once the drain is complete and prepares the replay.
func (s *bufferedSSEStream) finalize() {
	s.finalized = true
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.stopAfter()
	s.finalErr = io.EOF

	if s.tooLarge {
		s.report(ErrBufferLimit)
		s.replay = joinChunks(s.codec.Terminate(Termination{ErrorCode: "response_too_large", ErrorMessage: "streamed response exceeded the buffer limit"}))
		return
	}
	if s.finish == nil {
		s.replay = s.raw
	} else {
		replay, err := s.finish(s.events, s.raw)
		switch {
		case err != nil:
			s.report(err)
			s.replay = joinChunks(s.codec.Terminate(Termination{ErrorCode: "plugin_failure", ErrorMessage: "stream finisher failed"}))
			return
		case replay == nil:
			s.replay = s.raw
		default:
			s.replay = replay
		}
	}
	if s.drainErr != nil {
		s.finalErr = s.drainErr
	}
}

func (s *bufferedSSEStream) report(err error) {
	if s.opts.OnError != nil {
		s.opts.OnError(err)
	}
}

func joinChunks(chunks [][]byte) []byte {
	size := 0
	for _, chunk := range chunks {
		size += len(chunk)
	}
	out := make([]byte, 0, size)
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}
