package ext

import (
	"context"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedRewriter struct{ name string }

func (r *namedRewriter) Name() string { return r.name }

func (r *namedRewriter) Rewrite(_ context.Context, _ Input) (*Result, error) {
	return nil, nil
}

func TestRegistryPreservesRegistrationOrder(t *testing.T) {
	reg := &Registry{}
	names := []string{"first", "second", "third"}
	for _, name := range names {
		reg.RegisterRewriter(&namedRewriter{name: name})
	}

	got := reg.Rewriters()
	require.Len(t, got, len(names))
	for i, rw := range got {
		assert.Equal(t, names[i], rw.Name())
	}
}

func TestRegistrySnapshotsAreIsolated(t *testing.T) {
	reg := &Registry{}
	reg.RegisterRewriter(&namedRewriter{name: "one"})
	reg.AddPublicPaths("/sso/callback")

	rewriters := reg.Rewriters()
	paths := reg.PublicPaths()

	reg.RegisterRewriter(&namedRewriter{name: "two"})
	reg.AddPublicPaths("/sso/login")

	assert.Len(t, rewriters, 1, "earlier snapshot must not grow")
	assert.Equal(t, []string{"/sso/callback"}, paths)
	assert.Len(t, reg.Rewriters(), 2)
	assert.Len(t, reg.PublicPaths(), 2)
}

func TestRegistryCollectsMiddlewareAndRoutes(t *testing.T) {
	reg := &Registry{}
	reg.UseMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
	reg.RegisterRoutes(func(_ *echo.Echo) {})

	assert.Len(t, reg.Middleware(), 1)
	assert.Len(t, reg.Routes(), 1)
}

func TestRegistryConcurrentRegistration(t *testing.T) {
	reg := &Registry{}
	const workers = 16

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			reg.RegisterRewriter(&namedRewriter{name: "w"})
			reg.UseMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc { return next })
			reg.AddPublicPaths("/p")
			_ = reg.Rewriters()
		})
	}
	wg.Wait()

	assert.Len(t, reg.Rewriters(), workers)
	assert.Len(t, reg.Middleware(), workers)
	assert.Len(t, reg.PublicPaths(), workers)
}

func TestRejectionErrorMessage(t *testing.T) {
	err := &RejectionError{Status: 422, Code: "policy_violation", Message: "blocked by policy"}
	assert.Equal(t, "request rejected (422 policy_violation): blocked by policy", err.Error())
}
