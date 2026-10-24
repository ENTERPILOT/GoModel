package auditlog

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/stretchr/testify/require"
)

func TestAuditUserPathSubtreeBounds(t *testing.T) {
	tests := []struct {
		name                 string
		userPath             string
		wantLower, wantUpper string
	}{
		{name: "root spans every path", userPath: "/", wantLower: "/", wantUpper: "0"},
		{name: "nested path spans its descendants", userPath: "/team/a", wantLower: "/team/a/", wantUpper: "/team/a0"},
		{name: "wildcards need no escaping", userPath: "/team%_a", wantLower: "/team%_a/", wantUpper: "/team%_a0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lower, upper := auditUserPathSubtreeBounds(tt.userPath)
			require.Equal(t, tt.wantLower, lower)
			require.Equal(t, tt.wantUpper, upper, "auditUserPathSubtreeBounds(%q) = (%q, %q), want (%q, %q)", tt.userPath, lower, upper, tt.wantLower, tt.wantUpper)
		})
	}
}

func TestAuditUserPathSubtreeRegex(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		want     string
	}{
		{
			name:     "root matches full hierarchy",
			userPath: "/",
			want:     "^/",
		},
		{
			name:     "wildcards are treated literally",
			userPath: "/team%a",
			want:     "^/team%a(?:/|$)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auditUserPathSubtreeRegex(tt.userPath)
			require.Equal(t, tt.want, got, "auditUserPathSubtreeRegex(%q)", tt.userPath)
		})
	}
}

func TestEscapeLikeWildcards(t *testing.T) {
	require.Equal(t, "/team\\%\\_a", sqlutil.EscapeLikeWildcards("/team%_a"))
}

func TestAuditUserPathSQLPredicate(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		column   string
		want     string
	}{
		{
			name:     "root includes legacy null rows",
			userPath: "/",
			column:   "user_path",
			want:     "(user_path = ? OR (user_path >= ? AND user_path < ?) OR user_path IS NULL)",
		},
		{
			name:     "non-root excludes legacy null rows",
			userPath: "/team",
			column:   "user_path",
			want:     "(user_path = ? OR (user_path >= ? AND user_path < ?))",
		},
		{
			name:     "column expression is applied to every comparison",
			userPath: "/team",
			column:   `user_path COLLATE "C"`,
			want:     `(user_path COLLATE "C" = ? OR (user_path COLLATE "C" >= ? AND user_path COLLATE "C" < ?))`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auditUserPathSQLPredicate(tt.userPath, tt.column)
			require.Equal(t, tt.want, got, "auditUserPathSQLPredicate(%q)", tt.userPath)
		})
	}
}

func TestAuditExactUserPathSQLPredicate(t *testing.T) {
	require.Equal(t, "(user_path = ? OR user_path = '' OR user_path IS NULL)", auditExactUserPathSQLPredicate("/", "user_path"))
	require.Equal(t, "user_path = ?", auditExactUserPathSQLPredicate("/team", "user_path"))
}
