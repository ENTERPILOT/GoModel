package auditlog

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
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
			if lower != tt.wantLower || upper != tt.wantUpper {
				t.Fatalf("auditUserPathSubtreeBounds(%q) = (%q, %q), want (%q, %q)", tt.userPath, lower, upper, tt.wantLower, tt.wantUpper)
			}
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
			if got := auditUserPathSubtreeRegex(tt.userPath); got != tt.want {
				t.Fatalf("auditUserPathSubtreeRegex(%q) = %q, want %q", tt.userPath, got, tt.want)
			}
		})
	}
}

func TestEscapeLikeWildcards(t *testing.T) {
	if got := sqlutil.EscapeLikeWildcards("/team%_a"); got != "/team\\%\\_a" {
		t.Fatalf("sqlutil.EscapeLikeWildcards(%q) = %q, want %q", "/team%_a", got, "/team\\%\\_a")
	}
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
			if got := auditUserPathSQLPredicate(tt.userPath, tt.column); got != tt.want {
				t.Fatalf("auditUserPathSQLPredicate(%q) = %q, want %q", tt.userPath, got, tt.want)
			}
		})
	}
}

func TestAuditExactUserPathSQLPredicate(t *testing.T) {
	if got := auditExactUserPathSQLPredicate("/", "user_path"); got != "(user_path = ? OR user_path = '' OR user_path IS NULL)" {
		t.Fatalf("root exact predicate = %q", got)
	}
	if got := auditExactUserPathSQLPredicate("/team", "user_path"); got != "user_path = ?" {
		t.Fatalf("nested exact predicate = %q", got)
	}
}
