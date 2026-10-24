package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
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
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestUserPathFromContext_PrefersEffectiveOverride(t *testing.T) {
	ctx := WithRequestSnapshot(context.Background(), &RequestSnapshot{UserPath: "/team/from-header"})
	ctx = WithEffectiveUserPath(ctx, "/team/from-auth-key")
	got := UserPathFromContext(ctx)
	require.Equal(t, "/team/from-auth-key", got)
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
			got := UserPathHeaderName(tt.raw)
			require.Equal(t, tt.want, got, "UserPathHeaderName(%q)", tt.raw)
		})
	}
}

func TestUserPathHeaderNameFromContext(t *testing.T) {
	t.Parallel()
	got := UserPathHeaderNameFromContext(context.Background())
	require.Equal(t, UserPathHeader, got)

	customCtx := WithUserPathHeaderName(context.Background(), "x-tenant-path")
	got = UserPathHeaderNameFromContext(customCtx)
	require.Equal(t, "X-Tenant-Path", got)

	defaultCtx := WithUserPathHeaderName(customCtx, UserPathHeader)
	got = UserPathHeaderNameFromContext(defaultCtx)
	require.Equal(t, "X-Tenant-Path", got)
}

func TestUserPathAncestors(t *testing.T) {
	t.Parallel()

	got := UserPathAncestors("/team/a/user")
	want := []string{"/team/a/user", "/team/a", "/team", "/"}

	require.Equal(t, want, got)
}

func TestUserPathAncestors_Root(t *testing.T) {
	t.Parallel()

	got := UserPathAncestors("/")
	require.Len(t, got, 1)
	require.Equal(t, "/", got[0])
}

func TestUserPathChild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		base      string
		path      string
		want      string
		wantMatch bool
	}{
		{name: "direct child", base: "/users", path: "/users/alice", want: "/users/alice", wantMatch: true},
		{name: "deeper descendant", base: "/users", path: "/users/alice/app", want: "/users/alice", wantMatch: true},
		{name: "root template", base: "/", path: "/alice/app", want: "/alice", wantMatch: true},
		{name: "base itself", base: "/users", path: "/users"},
		{name: "sibling prefix", base: "/users", path: "/users-old/alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, matched := UserPathChild(tt.base, tt.path)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantMatch, matched)
		})
	}
}
