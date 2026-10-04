package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
// editors and `echo` add one.
func resolveFileSecret(_ context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("file path %s must be absolute", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxSecretFileSize {
		return "", fmt.Errorf("file %s is larger than 1 MiB", path)
	}
	value := string(data)
	if trimmed, ok := strings.CutSuffix(value, "\r\n"); ok {
		return trimmed, nil
	}
	return strings.TrimSuffix(value, "\n"), nil
}
