package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

type timeoutsModelResolver struct {
	timeouts core.UpstreamTimeouts
	route    []string
}

func (r *timeoutsModelResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	selector, err := requested.Normalize()
	return selector, false, err
}

func (r *timeoutsModelResolver) ResolveTimeouts(_ context.Context, _ core.RequestedModelSelector, _ core.ModelSelector, route []string) core.UpstreamTimeouts {
	r.route = route
	return r.timeouts
}

func TestWithModelTimeoutsAttachesResolvedTimeouts(t *testing.T) {
	resolver := &timeoutsModelResolver{timeouts: core.UpstreamTimeouts{Timeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second}}
	requested := core.NewRequestedModelSelector("voice", "")
	resolved := core.ModelSelector{Provider: "openai", Model: "tts-1"}

	ctx := withModelTimeouts(context.Background(), resolver, requested, resolved, []string{"voice", "inner"})

	assert.Equal(t, httpclient.AttemptTimeouts{Timeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second}, httpclient.AttemptTimeoutsFromContext(ctx))
	assert.Equal(t, []string{"voice", "inner"}, resolver.route, "the route from model selection reaches the resolver")
}

func TestWithModelTimeoutsWithoutResolverKeepsContext(t *testing.T) {
	ctx := context.Background()
	got := withModelTimeouts(ctx, nil, core.NewRequestedModelSelector("m", ""), core.ModelSelector{Model: "m"}, nil)
	assert.Equal(t, ctx, got)
}
