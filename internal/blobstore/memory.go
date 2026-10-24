package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

// Memory keeps blobs in process memory. It backs tests and the `memory`
// media storage type; nothing survives a restart.
type Memory struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{blobs: make(map[string][]byte)}
}

// Create returns a writer that publishes its buffer on commit.
func (s *Memory) Create(_ context.Context, key string) (Writer, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	return &memoryWriter{store: s, key: key}, nil
}

// Open returns a reader over a copy of the blob.
func (s *Memory) Open(_ context.Context, key string) (io.ReadSeekCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	s.mu.RLock()
	data, ok := s.blobs[key]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return nopSeekCloser{bytes.NewReader(bytes.Clone(data))}, nil
}

// Delete forgets the blob.
func (s *Memory) Delete(_ context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.blobs, key)
	s.mu.Unlock()
	return nil
}

// Close is a no-op.
func (s *Memory) Close() error {
	return nil
}

// Len reports how many blobs are stored; tests use it to check sweeps.
func (s *Memory) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.blobs)
}

type memoryWriter struct {
	store     *Memory
	key       string
	buf       bytes.Buffer
	committed bool
	closed    bool
}

func (w *memoryWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("write to closed blob writer")
	}
	return w.buf.Write(p)
}

func (w *memoryWriter) Commit() error {
	if w.committed {
		return nil
	}
	if w.closed {
		return errors.New("commit of closed blob writer")
	}
	w.store.mu.Lock()
	w.store.blobs[w.key] = bytes.Clone(w.buf.Bytes())
	w.store.mu.Unlock()
	w.committed = true
	w.closed = true
	return nil
}

func (w *memoryWriter) Close() error {
	w.closed = true
	w.buf.Reset()
	return nil
}

type nopSeekCloser struct {
	*bytes.Reader
}

func (nopSeekCloser) Close() error { return nil }
