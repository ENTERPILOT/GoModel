package sharedstore

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/ext"
)

// validateNamespace accepts 1 to ext.MaxSharedNamespaceBytes of a-z, 0-9,
// '.', '_' and '-'. The restricted alphabet keeps namespaces readable and
// leaves ':' free to join namespace and key into one MongoDB _id.
func validateNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("shared store namespace is empty")
	}
	if len(namespace) > ext.MaxSharedNamespaceBytes {
		return fmt.Errorf("shared store namespace is over %d bytes", ext.MaxSharedNamespaceBytes)
	}
	for _, r := range namespace {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return fmt.Errorf("shared store namespace %q may contain only a-z, 0-9, '.', '_' and '-'", namespace)
		}
	}
	return nil
}

// validateKey accepts 1 to ext.MaxSharedKeyBytes of valid UTF-8 without NUL,
// which PostgreSQL TEXT rejects.
func validateKey(key string) error {
	if key == "" {
		return errors.New("shared store key is empty")
	}
	if len(key) > ext.MaxSharedKeyBytes {
		return fmt.Errorf("shared store key is over %d bytes", ext.MaxSharedKeyBytes)
	}
	if !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return errors.New("shared store key must be valid UTF-8 without NUL bytes")
	}
	return nil
}
