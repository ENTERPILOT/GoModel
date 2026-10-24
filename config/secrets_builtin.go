package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sync/singleflight"
)

// maxSecretFileSize bounds a ${file:...} read. Secrets are short; a larger
// file is almost certainly the wrong path.
const maxSecretFileSize = 1 << 20

// resolveEnvSecret implements ${env:NAME}. Unlike the legacy ${NAME}, an unset
// or empty variable is an error rather than a literal left in place.
func resolveEnvSecret(_ context.Context, name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	if value == "" {
		return "", fmt.Errorf("environment variable %s is empty", name)
	}
	return value, nil
}

// resolveFileSecret implements ${file:/absolute/path}, the shape Docker and
// Kubernetes secret mounts take. One trailing newline is removed, because
// editors and `echo` add one. Like an empty ${env:NAME}, a file that is empty
// after that is an error: an emptied master key file must not turn
// authentication off.
func resolveFileSecret(_ context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("file path %s must be absolute", path)
	}
	// Non-blocking, so a FIFO with no writer does not hang open. A pipe could
	// not be re-read on reload or rotation anyway, so only regular files
	// count; symlinks to them, as Kubernetes mounts secrets, are followed.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("read %s: not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxSecretFileSize {
		return "", fmt.Errorf("file %s is larger than 1 MiB", path)
	}
	value, ok := strings.CutSuffix(string(data), "\r\n")
	if !ok {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return "", fmt.Errorf("file %s is empty", path)
	}
	return value, nil
}

// cancellable stops waiting for resolve once ctx ends, for a resolver whose
// I/O cannot observe ctx: reading a file on a stalled network mount blocks in
// the kernel. The abandoned call finishes in the background, and calls for a
// reference whose call is still running share it, so a stalled mount does
// not pile up blocked reads.
func cancellable(resolve SecretResolverFunc) SecretResolverFunc {
	var calls singleflight.Group
	return func(ctx context.Context, reference string) (string, error) {
		// Without cancellation: the call is shared, so one caller giving up
		// must not fail it for the others.
		shared := context.WithoutCancel(ctx)
		result := calls.DoChan(reference, func() (any, error) {
			return resolve(shared, reference)
		})
		select {
		case r := <-result:
			value, _ := r.Val.(string)
			return value, r.Err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
