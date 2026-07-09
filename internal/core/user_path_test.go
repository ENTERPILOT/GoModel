package core

import (
	"context"
	"testing"
)

func TestNormalizeUserPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty stays unset", raw: "", want: ""},
		{name: "trim add leading slash remove trailing slash", raw: " team/a/b/ ", want: "/team/a/b"},
		{name: "collapse repeated slashes", raw: "/team//a///b", want: "/team/a/b"},
		{name: "root stays root", raw: "/", want: "/"},
		{name: "reject current dir segment", raw: "/team/./a", wantErr: true},
		{name: "reject parent dir segment", raw: "/team/../a", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeUserPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NormalizeUserPath() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeUserPath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeUserPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserPathFromContext_PrefersEffectiveOverride(t *testing.T) {
	ctx := WithRequestSnapshot(context.Background(), &RequestSnapshot{UserPath: "/team/from-header"})
	ctx = WithEffectiveUserPath(ctx, "/team/from-auth-key")

	if got := UserPathFromContext(ctx); got != "/team/from-auth-key" {
		t.Fatalf("UserPathFromContext() = %q, want /team/from-auth-key", got)
	}
}

func TestUserPathHeaderName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty defaults", raw: "", want: UserPathHeader},
		{name: "default preserves GoModel spelling", raw: "x-gomodel-user-path", want: UserPathHeader},
		{name: "custom canonicalized", raw: "x-tenant-path", want: "X-Tenant-Path"},
		{name: "trim custom", raw: " X-Custom-User-Path ", want: "X-Custom-User-Path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := UserPathHeaderName(tt.raw); got != tt.want {
				t.Fatalf("UserPathHeaderName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestUserPathHeaderNameFromContext(t *testing.T) {
	t.Parallel()

	if got := UserPathHeaderNameFromContext(context.Background()); got != UserPathHeader {
		t.Fatalf("UserPathHeaderNameFromContext(empty) = %q, want %q", got, UserPathHeader)
	}

	customCtx := WithUserPathHeaderName(context.Background(), "x-tenant-path")
	if got := UserPathHeaderNameFromContext(customCtx); got != "X-Tenant-Path" {
		t.Fatalf("UserPathHeaderNameFromContext(custom) = %q, want X-Tenant-Path", got)
	}

	defaultCtx := WithUserPathHeaderName(customCtx, UserPathHeader)
	if got := UserPathHeaderNameFromContext(defaultCtx); got != "X-Tenant-Path" {
		t.Fatalf("UserPathHeaderNameFromContext(default no-op) = %q, want X-Tenant-Path", got)
	}
}

func TestUserPathAncestors(t *testing.T) {
	t.Parallel()

	got := UserPathAncestors("/team/a/user")
	want := []string{"/team/a/user", "/team/a", "/team", "/"}

	if len(got) != len(want) {
		t.Fatalf("len(UserPathAncestors()) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UserPathAncestors()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUserPathAncestors_Root(t *testing.T) {
	t.Parallel()

	got := UserPathAncestors("/")
	if len(got) != 1 {
		t.Fatalf("len(UserPathAncestors(\"/\")) = %d, want 1 (%v)", len(got), got)
	}
	if got[0] != "/" {
		t.Fatalf("UserPathAncestors(\"/\")[0] = %q, want /", got[0])
	}
}
