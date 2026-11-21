package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestNewInstance(t *testing.T) {
	schema := []pluginapi.Field{{Key: "content", Input: pluginapi.InputText, Required: true}}
	tests := []struct {
		name    string
		plugin  *fakePlugin
		spec    InstanceSpec
		wantErr string
	}{
		{name: "valid", plugin: &fakePlugin{name: "p", schema: schema}, spec: InstanceSpec{Name: "i", Config: json.RawMessage(`{"content":"x"}`)}},
		{name: "invalid config", plugin: &fakePlugin{name: "p", schema: schema}, spec: InstanceSpec{Name: "i", Config: json.RawMessage(`{}`)}, wantErr: "required"},
		{name: "init error", plugin: &fakePlugin{name: "p", initErr: errFake}, spec: InstanceSpec{Name: "i"}, wantErr: "fake failure"},
		{name: "empty name", plugin: &fakePlugin{name: "p"}, spec: InstanceSpec{Name: " "}, wantErr: "instance name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst, err := NewInstance(context.Background(), newEntry(tt.plugin), tt.spec, NewHost(HostDeps{}, HostInfo{}))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.plugin.initErr != nil {
					require.True(t, tt.plugin.closed, "a plugin whose Init failed must be closed")
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, "p", inst.Type)
			require.Equal(t, "i", inst.Name)
			require.NotEmpty(t, inst.ConfigHash)
			require.NotNil(t, tt.plugin.lastHost)
			require.Equal(t, `{"content":"x"}`, string(tt.plugin.config), "init config = %s", tt.plugin.config)
			err = inst.Close(context.Background())
			require.NoError(t, err)
			require.True(t, tt.plugin.closed)
		})
	}
}

func TestNewInstanceRecoversInitPanic(t *testing.T) {
	entry := Entry{Name: "boom", Factory: func() pluginapi.Plugin { return &panicInit{} }}
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	require.ErrorContains(t, err, "panicked")
}

type panicInit struct{ fakePlugin }

func (*panicInit) Init(context.Context, json.RawMessage, pluginapi.Host) error { panic("init boom") }

func TestFailModes(t *testing.T) {
	mode, err := ParseFailMode(" Open ")
	require.NoError(t, err)
	require.Equal(t, FailOpen, mode)
	_, err = ParseFailMode("maybe")
	require.Error(t, err)

	inst := newTestInstance(&fakePlugin{name: "p"}, InstanceSpec{})
	require.Equal(t, FailClosed, inst.EffectiveFailMode(pluginapi.KindPrompt))
	require.Equal(t, FailOpen, inst.EffectiveFailMode(pluginapi.KindRequest))

	inst.FailMode = FailOpen
	require.Equal(t, FailOpen, inst.EffectiveFailMode(pluginapi.KindPrompt))
}

func TestBuildChain(t *testing.T) {
	reader := newTestInstance(&fakePlugin{name: "reader"}, InstanceSpec{})
	editorA := newTestInstance(&fakePlugin{name: "editor-a", mutates: true}, InstanceSpec{})
	editorB := newTestInstance(&fakePlugin{name: "editor-b", mutates: true}, InstanceSpec{})
	promptOnlyInst, err := NewInstance(context.Background(), newEntry(&promptOnly{fakePlugin{name: "prompt-only", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}}), InstanceSpec{Name: "prompt-only"}, NewHost(HostDeps{}, HostInfo{}))
	require.NoError(t, err)

	promptOnlyInst.Kinds = []pluginapi.Kind{pluginapi.KindPrompt}

	tests := []struct {
		name    string
		phase   pluginapi.Kind
		refs    []Ref
		wantErr string
		steps   []int
	}{
		{name: "empty", phase: pluginapi.KindPrompt},
		{name: "sorted steps", phase: pluginapi.KindPrompt, refs: []Ref{{editorA, 20}, {reader, 10}, {editorB, 10}}, steps: []int{10, 20}},
		{name: "two mutators in a step", phase: pluginapi.KindPrompt, refs: []Ref{{editorA, 10}, {editorB, 10}}, wantErr: "mutating"},
		{name: "missing hook", phase: pluginapi.KindResponse, refs: []Ref{{promptOnlyInst, 10}}, wantErr: "does not implement the response hook"},
		{name: "duplicate instance", phase: pluginapi.KindPrompt, refs: []Ref{{reader, 10}, {reader, 20}}, wantErr: "twice"},
		{name: "not a phase", phase: pluginapi.KindRoute, refs: []Ref{{reader, 10}}, wantErr: "not a chain phase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, err := BuildChain(tt.phase, tt.refs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			if len(tt.refs) == 0 {
				require.Nil(t, chain)
				return
			}
			require.Len(t, chain.Steps, len(tt.steps))

			for i, order := range tt.steps {
				require.Equal(t, order, chain.Steps[i].Order)
			}
			require.NotEmpty(t, chain.Hash)
			require.Equal(t, len(tt.refs), chain.Len())
		})
	}
}

func TestChainHashChangesWithConfigStepAndFailMode(t *testing.T) {
	build := func(config string, step int, mode FailMode) string {
		inst := newTestInstance(&fakePlugin{name: "p", schema: []pluginapi.Field{{Key: "v", Input: pluginapi.InputText}}}, InstanceSpec{Config: json.RawMessage(config), FailMode: mode})
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, step}})
		require.NoError(t, err)

		return chain.Hash
	}
	base := build(`{"v":"a"}`, 10, "")
	require.Equal(t, build(`{"v":"a"}`, 10, ""), base)

	for name, other := range map[string]string{
		"config":   build(`{"v":"b"}`, 10, ""),
		"step":     build(`{"v":"a"}`, 20, ""),
		"failmode": build(`{"v":"a"}`, 10, FailOpen),
	} {
		require.NotEqual(t, base, other, "hash unchanged for %s", name)
	}
	chains := &Chains{Prompt: &Chain{Hash: "p", Steps: []Step{{Order: 1}}}}
	got := chains.Hashes()
	require.Len(t, got, 1)
	require.Equal(t, "p", got["prompt"])
	require.False(t, chains.Empty())
	require.Equal(t, "p", chains.PromptHash())
}

func TestComputeChainHashFormat(t *testing.T) {
	require.Empty(t, ComputeChainHash(nil))

	a := ComputeChainHash([]RuleDescriptor{{Name: "a", Type: "t", Order: 1, Mode: "closed", Content: "x"}, {Name: "b", Type: "t", Order: 2, Mode: "closed", Content: "y"}})
	b := ComputeChainHash([]RuleDescriptor{{Name: "b", Type: "t", Order: 2, Mode: "closed", Content: "y"}, {Name: "a", Type: "t", Order: 1, Mode: "closed", Content: "x"}})
	require.Equal(t, b, a)
	require.Len(t, a, 64)
}

func TestInstanceStreamPolicyDefaultsToObserve(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "s"}, InstanceSpec{})
	require.Equal(t, pluginapi.StreamObserve, inst.StreamPolicy().Mode)

	inst.Timeout = time.Second
	require.Equal(t, time.Second, inst.Timeout)
}

type panickingPolicy struct{ *fakePlugin }

func (panickingPolicy) StreamPolicy() pluginapi.StreamPolicy { panic("policy boom") }

func TestInstanceStreamPolicyRecoversPanic(t *testing.T) {
	inst := &Instance{Name: "s", Type: "s", Plugin: panickingPolicy{&fakePlugin{name: "s"}}, Kinds: []pluginapi.Kind{pluginapi.KindStream}}
	got := inst.StreamPolicy().Mode
	require.Equal(t, pluginapi.StreamObserve, got)
}

func TestChainsCacheHash(t *testing.T) {
	promptOnly := &Chains{Prompt: &Chain{Hash: "p", Steps: []Step{{Order: 1}}}}
	require.Equal(t, "p", promptOnly.CacheHash())

	withResponse := &Chains{Prompt: promptOnly.Prompt, Response: &Chain{Hash: "r", Steps: []Step{{Order: 1}}}}
	other := &Chains{Prompt: promptOnly.Prompt, Response: &Chain{Hash: "r2", Steps: []Step{{Order: 1}}}}
	require.NotEqual(t, "p", withResponse.CacheHash())
	require.NotEqual(t, other.CacheHash(), withResponse.CacheHash())
	require.Len(t, withResponse.CacheHash(), 64)
	require.Empty(t, (&Chains{}).CacheHash())
	require.Empty(t, (*Chains)(nil).CacheHash())
}

func TestChainsFilter(t *testing.T) {
	fixed := newTestInstance(&fakePlugin{name: "fixed"}, InstanceSpec{})
	offered := newTestInstance(&fakePlugin{name: "offered", mutates: true}, InstanceSpec{})
	scan := newTestInstance(&fakePlugin{name: "scan"}, InstanceSpec{})
	prompt, err := BuildChain(pluginapi.KindPrompt, []Ref{{fixed, 10}, {offered, 20}})
	require.NoError(t, err)
	response, err := BuildChain(pluginapi.KindResponse, []Ref{{offered, 10}, {scan, 10}})
	require.NoError(t, err)
	all := &Chains{Prompt: prompt, Response: response}

	names := func(chain *Chain) []string {
		out := []string{}
		for _, inst := range chain.Instances() {
			out = append(out, inst.Name)
		}
		return out
	}

	// Leaving a member out of one phase keeps it in the others and keeps
	// every remaining member at its step.
	filtered, err := all.Filter(func(phase pluginapi.Kind, instance string) bool {
		return phase != pluginapi.KindPrompt || instance != "offered"
	})
	require.NoError(t, err)
	require.Equal(t, []string{"fixed"}, names(filtered.Prompt))
	require.Equal(t, 10, filtered.Prompt.Steps[0].Order)
	require.Equal(t, []string{"offered", "scan"}, names(filtered.Response))
	require.Nil(t, filtered.Stream)
	require.NotEqual(t, all.Prompt.Hash, filtered.Prompt.Hash, "a chain without a member is a different chain")
	require.Equal(t, all.Response.Hash, filtered.Response.Hash)
	require.NotEqual(t, all.CacheHash(), filtered.CacheHash())

	// A phase left without members has no chain.
	none, err := all.Filter(func(pluginapi.Kind, string) bool { return false })
	require.NoError(t, err)
	require.True(t, none.Empty())

	var missing *Chains
	filtered, err = missing.Filter(func(pluginapi.Kind, string) bool { return true })
	require.NoError(t, err)
	require.Nil(t, filtered)
}
