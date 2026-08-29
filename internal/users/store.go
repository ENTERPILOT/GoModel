package users

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/validation"
)

var (
	// ErrUserNotFound indicates a requested user record does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrGroupNotFound indicates a requested group record does not exist.
	ErrGroupNotFound = errors.New("group not found")
)

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// Store defines persistence operations for users and groups.
type Store interface {
	ListUsers(ctx context.Context) ([]User, error)
	UpsertUser(ctx context.Context, user User) error
	DeleteUser(ctx context.Context, id string) error
	ListGroups(ctx context.Context) ([]Group, error)
	UpsertGroup(ctx context.Context, group Group) error
	DeleteGroup(ctx context.Context, name string) error
	Close() error
}

// NormalizeGroupName canonicalizes one group name. Names are verbatim,
// case-sensitive identifiers; "/" is rejected so a group can never be
// mistaken for a user path, and "," so names survive comma-separated lists.
func NormalizeGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", newValidationError("group name is required", nil)
	}
	if strings.ContainsAny(name, "/,") {
		return "", newValidationError("group name cannot contain '/' or ','", nil)
	}
	return name, nil
}

// NormalizeGroupNames trims, dedupes, and sorts group names. Empty input is
// allowed and yields nil.
func NormalizeGroupNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(names))
	normalized := make([]string, 0, len(names))
	for _, raw := range names {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		name, err := NormalizeGroupName(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

// normalizeUserName validates a user name as a path segment: the derived
// user_path is the group path plus this name.
func normalizeUserName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", newValidationError("name is required", nil)
	}
	if strings.Contains(name, "/") {
		return "", newValidationError("name cannot contain '/'", nil)
	}
	return name, nil
}

// derivedPath joins a parent hierarchy path and one segment. An empty parent
// means the root.
func derivedPath(parentPath, segment string) (string, error) {
	path, err := core.NormalizeUserPath(parentPath + "/" + segment)
	if err != nil {
		return "", newValidationError("invalid name "+segment, err)
	}
	return path, nil
}

func normalizeID(id string) string {
	return strings.TrimSpace(id)
}

type userScanner interface {
	Scan(dest ...any) error
}

type userRows interface {
	userScanner
	Next() bool
	Err() error
}

func collectRows[T any](rows userRows, scan func(userScanner) (T, error)) ([]T, error) {
	result := make([]T, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func wrapStoreErr(op string, err error) error {
	return fmt.Errorf("%s: %w", op, err)
}
