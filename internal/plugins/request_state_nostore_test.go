package plugins

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

func TestRequestStateNoStore(t *testing.T) {
	var nilState *RequestState
	if nilState.NoStore() {
		t.Fatal("nil state must not veto")
	}

	state := NewRequestState()
	state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "a", Decision: pluginapi.Allow()})
	if state.NoStore() {
		t.Fatal("allow without NoStore must not veto")
	}

	veto := pluginapi.Warn("pii", "restored", nil)
	veto.NoStore = true
	state.Record(
		DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "b", Decision: pluginapi.Allow()},
		DecisionRecord{Phase: pluginapi.KindResponse, Instance: "c", Decision: veto},
	)
	if !state.NoStore() {
		t.Fatal("a recorded NoStore decision must veto")
	}
	state.Record(DecisionRecord{Phase: pluginapi.KindStream, Instance: "d", Decision: pluginapi.Allow()})
	if !state.NoStore() {
		t.Fatal("a later allow must not clear the veto")
	}

	// The cache reaches the veto through the workflow the state was created on.
	workflow := &core.Workflow{}
	ctx := core.WithWorkflow(context.Background(), workflow)
	if core.PluginNoStore(ctx) {
		t.Fatal("no plugin state yet: must not veto")
	}
	got := RequestStateFor(ctx)
	if core.PluginNoStore(ctx) {
		t.Fatal("fresh state must not veto")
	}
	got.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "e", Decision: veto})
	if !core.PluginNoStore(ctx) {
		t.Fatal("PluginNoStore must see the veto through Workflow.PluginState")
	}
	if core.PluginNoStore(context.Background()) {
		t.Fatal("no workflow: must not veto")
	}
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
	if err != nil {
		t.Fatal(err)
	}

	state := NewRequestState()
	x := withPromptText(state.NewExchange(context.Background(), pluginapi.Meta{}), "hello")
	outcome, err := chain.RunPrompt(context.Background(), x)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if outcome.Decision.Action != pluginapi.ActionWarn || outcome.Decision.NoStore {
		t.Fatalf("merged decision = %+v; the warn from the mutator wins and carries no veto itself", outcome.Decision)
	}
	for _, record := range outcome.Records {
		state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: record.Instance, Decision: record.Decision})
	}
	if !state.NoStore() {
		t.Fatal("the reader's NoStore must survive the severity merge through the records")
	}
}
