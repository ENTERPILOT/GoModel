package auditlog

import "testing"

func TestAuditUserPathSubtreePattern(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		want     string
	}{
		{
			name:     "root matches full subtree",
			userPath: "/",
			want:     "/%",
		},
		{
			name:     "nested path appends descendant wildcard",
			userPath: "/team/a",
			want:     "/team/a/%",
		},
		{
			name:     "percent is escaped before subtree wildcard",
			userPath: "/team%a",
			want:     "/team\\%a/%",
		},
		{
			name:     "underscore is escaped before subtree wildcard",
			userPath: "/team_a",
			want:     "/team\\_a/%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auditUserPathSubtreePattern(tt.userPath); got != tt.want {
				t.Fatalf("auditUserPathSubtreePattern(%q) = %q, want %q", tt.userPath, got, tt.want)
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
	if got := escapeLikeWildcards("/team%_a"); got != "/team\\%\\_a" {
		t.Fatalf("escapeLikeWildcards(%q) = %q, want %q", "/team%_a", got, "/team\\%\\_a")
	}
}

func TestAuditUserPathSQLPredicate(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		want     string
	}{
		{
			name:     "root includes legacy null rows",
			userPath: "/",
			want:     "(user_path = ? OR user_path LIKE ? ESCAPE '\\' OR user_path IS NULL)",
		},
		{
			name:     "non-root excludes legacy null rows",
			userPath: "/team",
			want:     "(user_path = ? OR user_path LIKE ? ESCAPE '\\')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auditUserPathSQLPredicate(tt.userPath, "user_path = ?", "user_path LIKE ? ESCAPE '\\'"); got != tt.want {
				t.Fatalf("auditUserPathSQLPredicate(%q) = %q, want %q", tt.userPath, got, tt.want)
			}
		})
	}
}
