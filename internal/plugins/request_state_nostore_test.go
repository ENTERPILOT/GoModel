package plugins

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

func TestRequestStateNoStore(t *testing.T) {
	var nilState *RequestState
	require.False(t, nilState.NoStore())

	state := NewRequestState()
	state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "a", Decision: pluginapi.Allow()})
	require.False(t, state.NoStore())

	veto := pluginapi.Warn("pii", "restored", nil)
	veto.NoStore = true
	state.Record(
		DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "b", Decision: pluginapi.Allow()},
		DecisionRecord{Phase: pluginapi.KindResponse, Instance: "c", Decision: veto},
	)
	require.True(t, state.NoStore())

	state.Record(DecisionRecord{Phase: pluginapi.KindStream, Instance: "d", Decision: pluginapi.Allow()})
	require.True(t, state.NoStore())

	// The cache reaches the veto through the workflow the state was created on.
	workflow := &core.Workflow{}
	ctx := core.WithWorkflow(context.Background(), workflow)
	require.False(t, core.PluginNoStore(ctx))

	got := RequestStateFor(ctx)
	require.False(t, core.PluginNoStore(ctx))

	got.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "e", Decision: veto})
	require.True(t, core.PluginNoStore(ctx))
	require.False(t, core.PluginNoStore(context.Background()))
}

func TestChainOutcomeCarriesNoStoreFromReaders(t *testing.T) {
	veto := pluginapi.Allow()
	veto.NoStore = true
	reader := newTestInstance(&fakePlugin{name: "reader", kinds: []pluginapi.Kind{pluginapi.KindPrompt}, onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
		return veto, nil
	}}, InstanceSpec{})
	mutator := newTestInstance(&fakePlugin{name: "mutator", kinds: []pluginapi.Kind{pluginapi.KindPrompt}, mutates: true, onPrompt: func(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
		return pluginapi.Warn("w", "", nil), nil
	}}, InstanceSpec{})
	chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{reader, 10}, {mutator, 10}})
	require.NoError(t, err)

	state := NewRequestState()
	x := withPromptText(state.NewExchange(context.Background(), pluginapi.Meta{}), "hello")
	outcome, err := chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionWarn, outcome.Decision.Action)
	require.False(t, outcome.Decision.NoStore, "merged decision = %+v; the warn from the mutator wins and carries no veto itself", outcome.Decision)

	for _, record := range outcome.Records {
		state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: record.Instance, Decision: record.Decision})
	}
	require.True(t, state.NoStore())
}
