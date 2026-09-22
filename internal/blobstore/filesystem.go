package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Filesystem stores each blob as a file under a root directory, at the path
// its key spells. Writes land in a temporary file beside the target and are
// renamed into place on commit, so a reader never sees a partial blob and a
// crash mid-write leaves nothing but a stray temp file.
type Filesystem struct {
	root string
}

// NewFilesystem creates the root directory if needed and returns a store
// rooted there.
func NewFilesystem(root string) (*Filesystem, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("blob root directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve blob root %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create blob root %q: %w", abs, err)
	}
	return &Filesystem{root: abs}, nil
}

// Root is the absolute directory blobs live under.
func (s *Filesystem) Root() string {
	return s.root
}

func (s *Filesystem) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// Create opens a temporary file next to the target path.
func (s *Filesystem) Create(_ context.Context, key string) (Writer, error) {
	target, err := s.path(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create blob temp file: %w", err)
	}
	return &fileWriter{file: tmp, target: target}, nil
}

// Open returns the blob's file.
func (s *Filesystem) Open(_ context.Context, key string) (io.ReadSeekCloser, error) {
	target, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open blob: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat blob: %w", err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, ErrNotFound
	}
	return f, nil
}

// Delete removes the blob's file and any directories it leaves empty, up to
// the root, so date-partitioned keys do not accumulate empty folders.
func (s *Filesystem) Delete(_ context.Context, key string) error {
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob: %w", err)
	}
	for dir := filepath.Dir(target); dir != s.root && strings.HasPrefix(dir, s.root); dir = filepath.Dir(dir) {
		// Remove refuses a non-empty directory, which is exactly the stop
		// condition wanted here.
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}

// Close is a no-op; files need no teardown.
func (s *Filesystem) Close() error {
	return nil
}

type fileWriter struct {
	file      *os.File
	target    string
	committed bool
	closed    bool
}

func (w *fileWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("write to closed blob writer")
	}
	return w.file.Write(p)
}

// Commit flushes the temp file and renames it into place.
func (w *fileWriter) Commit() error {
	if w.committed {
		return nil
	}
	if w.closed {
		return errors.New("commit of closed blob writer")
	}
	if err := w.file.Sync(); err != nil {
		w.discard()
		return fmt.Errorf("sync blob: %w", err)
	}
	if err := w.file.Close(); err != nil {
		w.discard()
		return fmt.Errorf("close blob: %w", err)
	}
	if err := os.Rename(w.file.Name(), w.target); err != nil {
		_ = os.Remove(w.file.Name())
		w.closed = true
		return fmt.Errorf("publish blob: %w", err)
	}
	w.committed = true
	w.closed = true
	return nil
}

// Close discards an uncommitted blob.
func (w *fileWriter) Close() error {
	if w.closed {
		return nil
	}
	w.discard()
	return nil
}

func (w *fileWriter) discard() {
	_ = w.file.Close()
	_ = os.Remove(w.file.Name())
	w.closed = true
}
