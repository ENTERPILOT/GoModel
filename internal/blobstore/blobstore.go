// Package blobstore keeps opaque byte payloads (audio, images, video) outside
// the database. A Store addresses blobs by key; the metadata that makes a blob
// meaningful (content type, owner, expiry) lives in internal/mediastore.
package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound reports a key with no blob behind it.
var ErrNotFound = errors.New("blob not found")

// Store persists blobs by key.
type Store interface {
	// Create opens a writer for key. The blob becomes visible only when the
	// writer is committed; closing an uncommitted writer discards what was
	// written. An existing blob at key is replaced on commit.
	Create(ctx context.Context, key string) (Writer, error)
	// Open returns the blob at key for reading. It is the caller's job to
	// close the reader.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	// Delete removes the blob at key. A missing blob is not an error.
	Delete(ctx context.Context, key string) error
	// Close releases backend resources.
	Close() error
}

// Writer receives one blob. Commit publishes it; Close without a prior
// Commit discards it. Both are safe to call more than once, and Close after
// Commit is a no-op, so `defer w.Close()` is the right shape for callers.
type Writer interface {
	io.Writer
	Commit() error
	Close() error
}

// Put stores everything r yields under key and reports the byte count.
func Put(ctx context.Context, store Store, key string, r io.Reader) (int64, error) {
	w, err := store.Create(ctx, key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = w.Close() }()
	n, err := io.Copy(w, r)
	if err != nil {
		return n, err
	}
	if err := w.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// ValidateKey accepts keys made of slash-separated segments of
// [A-Za-z0-9._-], none empty and none "." or "..", so every key is safe to
// join under a root directory and stays portable across filesystems and
// object stores.
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("blob key is empty")
	}
	for segment := range strings.SplitSeq(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("blob key %q has an invalid path segment", key)
		}
		for _, r := range segment {
			if !keyRune(r) {
				return fmt.Errorf("blob key %q contains %q", key, r)
			}
		}
	}
	return nil
}

func keyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '_', r == '-':
		return true
	default:
		return false
	}
}
