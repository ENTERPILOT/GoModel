package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
)

type autoRoutesAwareRewriter struct {
	plainRewriter
	routes ext.AutoRoutes
}

func (r *autoRoutesAwareRewriter) SetAutoRoutes(routes ext.AutoRoutes) { r.routes = routes }

type fixedAutoRoutes struct{}

func (fixedAutoRoutes) AutoRoute(model, _ string) (ext.AutoRoute, bool) {
	return ext.AutoRoute{Source: model, Levels: ext.RouteLevels()}, model == "auto"
}

func TestBindRewriterAutoRoutes(t *testing.T) {
	bindRewriterAutoRoutes(nil, fixedAutoRoutes{})

	registry := &ext.Registry{}
	var typedNil *autoRoutesAwareRewriter
	registry.RegisterRewriter(typedNil)
	registry.RegisterRewriter(plainRewriter{})
	router := &autoRoutesAwareRewriter{}
	registry.RegisterRewriter(router)

	// A missing lookup leaves the rewriter as it is.
	bindRewriterAutoRoutes(registry, nil)
	assert.Nil(t, router.routes)

	bindRewriterAutoRoutes(registry, fixedAutoRoutes{})
	require.NotNil(t, router.routes)
	route, ok := router.routes.AutoRoute("auto", "")
	require.True(t, ok)
	assert.Equal(t, ext.RouteLevels(), route.Levels)
}
