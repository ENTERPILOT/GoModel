package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAccessScopeAllows(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		path  string
		want  bool
	}{
		{name: "global scope admits everything", scope: "", path: "/team/alpha", want: true},
		{name: "global scope admits empty path", scope: "", path: "", want: true},
		{name: "root scope is global", scope: "/", path: "", want: true},
		{name: "scope root itself", scope: "/team/alpha", path: "/team/alpha", want: true},
		{name: "descendant", scope: "/team/alpha", path: "/team/alpha/service/worker", want: true},
		{name: "sibling with shared prefix", scope: "/team/alpha", path: "/team/alpha-2", want: false},
		{name: "ancestor", scope: "/team/alpha", path: "/team", want: false},
		{name: "unrelated tenant", scope: "/team/alpha", path: "/team/beta", want: false},
		{name: "empty path hidden from scoped", scope: "/team/alpha", path: "", want: false},
		{name: "root path hidden from scoped", scope: "/team/alpha", path: "/", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := AccessScope{UserPath: tt.scope}
			assert.Equal(t, tt.want, scope.Allows(tt.path))
		})
	}
}

func TestUserPathContains(t *testing.T) {
	assert.True(t, UserPathContains("/", "/anything"))
	assert.False(t, UserPathContains("/", ""))
	assert.False(t, UserPathContains("", "/team"))
	assert.True(t, UserPathContains("/team", "/team"))
	assert.True(t, UserPathContains("/team", "/team/x"))
	assert.False(t, UserPathContains("/team", "/teams"))
}

func TestAccessScopeContext(t *testing.T) {
	assert.True(t, AccessScopeFromContext(context.Background()).Global())
	assert.True(t, AccessScopeFromContext(nil).Global()) //nolint:staticcheck // nil context must be safe

	ctx := WithAccessScope(context.Background(), AccessScope{UserPath: "team/alpha/"})
	scope := AccessScopeFromContext(ctx)
	assert.Equal(t, "/team/alpha", scope.UserPath, "scope path is canonicalized")
	assert.False(t, scope.Global())

	// A later global scope replaces the narrower one (explicit bearer over an
	// ambient extension session).
	ctx = WithAccessScope(ctx, AccessScope{})
	assert.True(t, AccessScopeFromContext(ctx).Global())
}
