package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRouteLevelRank(t *testing.T) {
	t.Parallel()
	for i, level := range RouteLevels() {
		assert.Equal(t, i+1, RouteLevelRank(level), level)
	}
	assert.Equal(t, 3, RouteLevelRank(" Smart "))
	assert.Zero(t, RouteLevelRank(""))
	assert.Zero(t, RouteLevelRank("genius"))
}

func TestRouteLevelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert.Empty(t, RouteLevelFromContext(ctx))
	assert.Equal(t, RouteLevelSmart, RouteLevelFromContext(WithRouteLevel(ctx, " SMART ")))
	assert.Empty(t, RouteLevelFromContext(WithRouteLevel(ctx, "genius")), "an unknown level is not carried")
}

func TestRouteLevelName(t *testing.T) {
	t.Parallel()
	for _, level := range RouteLevels() {
		assert.Equal(t, level, RouteLevelName(RouteLevelRank(level)))
	}
	assert.Empty(t, RouteLevelName(0))
	assert.Empty(t, RouteLevelName(5))

	levels := RouteLevels()
	levels[0] = "changed"
	assert.Equal(t, RouteLevelLight, RouteLevels()[0], "callers get their own copy")
}

func TestWithoutRouteLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert.Equal(t, ctx, WithoutRouteLevel(ctx), "a context without a level is returned as is")
	assert.Empty(t, RouteLevelFromContext(WithoutRouteLevel(WithRouteLevel(ctx, RouteLevelLight))))
}

func TestServedRouteLevel(t *testing.T) {
	t.Parallel()
	// Without a holder a report goes nowhere.
	ReportServedRouteLevel(context.Background(), 2)

	ctx, served := WithServedRouteLevel(context.Background())
	assert.Empty(t, served.Level)
	ReportServedRouteLevel(ctx, RouteLevelRank(RouteLevelSmart))
	assert.Equal(t, RouteLevelSmart, served.Level)
}
