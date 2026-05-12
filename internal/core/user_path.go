package core

import (
	"context"
	"fmt"
	"net/textproto"
	"strings"
)

const UserPathHeader = "X-GoModel-User-Path"

// UserPathHeaderName canonicalizes the configured user-path header name.
func UserPathHeaderName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return UserPathHeader
	}
	if strings.EqualFold(raw, UserPathHeader) {
		return UserPathHeader
	}
	return textproto.CanonicalMIMEHeaderKey(raw)
}

// UserPathHeaderNameFromContext returns the request-scoped user-path header
// name, falling back to the default public header.
func UserPathHeaderNameFromContext(ctx context.Context) string {
	if ctx != nil {
		if value, ok := ctx.Value(userPathHeaderNameKey).(string); ok {
			return UserPathHeaderName(value)
		}
	}
	return UserPathHeader
}

// NormalizeUserPath canonicalizes one user hierarchy path from request ingress.
func NormalizeUserPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}

	segments := strings.Split(raw, "/")
	canonical := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		switch segment {
		case ".", "..":
			return "", fmt.Errorf("user path cannot contain '.' or '..' segments")
		}
		if strings.Contains(segment, ":") {
			return "", fmt.Errorf("user path cannot contain ':'")
		}
		canonical = append(canonical, segment)
	}

	if len(canonical) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(canonical, "/"), nil
}

// UserPathAncestors returns deepest-to-root path fallback candidates.
func UserPathAncestors(path string) []string {
	path, err := NormalizeUserPath(path)
	if err != nil || path == "" {
		return nil
	}
	if path == "/" {
		return []string{"/"}
	}

	ancestors := []string{path}
	current := path
	for current != "/" {
		idx := strings.LastIndex(current, "/")
		if idx <= 0 {
			current = "/"
		} else {
			current = current[:idx]
		}
		ancestors = append(ancestors, current)
	}
	return ancestors
}

// UserPathFromContext returns the canonical request user path when available.
func UserPathFromContext(ctx context.Context) string {
	if userPath := GetEffectiveUserPath(ctx); userPath != "" {
		return userPath
	}
	if snapshot := GetRequestSnapshot(ctx); snapshot != nil {
		return snapshot.UserPath
	}
	return ""
}
