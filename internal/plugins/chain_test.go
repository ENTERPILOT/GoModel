package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewInstance() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewInstance() error = %v", err)
			}
			if inst.Type != "p" || inst.Name != "i" || inst.ConfigHash == "" || tt.plugin.lastHost == nil {
				t.Fatalf("instance = %+v", inst)
			}
			if string(tt.plugin.config) != `{"content":"x"}` {
				t.Fatalf("init config = %s", tt.plugin.config)
			}
			if err := inst.Close(context.Background()); err != nil || !tt.plugin.closed {
				t.Fatalf("Close() error = %v, closed = %v", err, tt.plugin.closed)
			}
		})
	}
}

func TestNewInstanceRecoversInitPanic(t *testing.T) {
	entry := Entry{Name: "boom", Factory: func() pluginapi.Plugin { return &panicInit{} }}
	_, err := NewInstance(context.Background(), entry, InstanceSpec{Name: "i"}, NewHost(HostDeps{}, HostInfo{}))
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error = %v", err)
	}
}

type panicInit struct{ fakePlugin }

func (*panicInit) Init(context.Context, json.RawMessage, pluginapi.Host) error { panic("init boom") }

func TestFailModes(t *testing.T) {
	if mode, err := ParseFailMode(" Open "); err != nil || mode != FailOpen {
		t.Fatalf("ParseFailMode(Open) = %q, %v", mode, err)
	}
	if _, err := ParseFailMode("maybe"); err == nil {
		t.Fatal("ParseFailMode(maybe) error = nil")
	}
	inst := newTestInstance(&fakePlugin{name: "p"}, InstanceSpec{})
	if inst.EffectiveFailMode(pluginapi.KindPrompt) != FailClosed || inst.EffectiveFailMode(pluginapi.KindRequest) != FailOpen {
		t.Fatal("default fail modes wrong")
	}
	inst.FailMode = FailOpen
	if inst.EffectiveFailMode(pluginapi.KindPrompt) != FailOpen {
		t.Fatal("explicit fail mode ignored")
	}
}

func TestBuildChain(t *testing.T) {
	reader := newTestInstance(&fakePlugin{name: "reader"}, InstanceSpec{})
	editorA := newTestInstance(&fakePlugin{name: "editor-a", mutates: true}, InstanceSpec{})
	editorB := newTestInstance(&fakePlugin{name: "editor-b", mutates: true}, InstanceSpec{})
	promptOnlyInst, err := NewInstance(context.Background(), newEntry(&promptOnly{fakePlugin{name: "prompt-only", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}}), InstanceSpec{Name: "prompt-only"}, NewHost(HostDeps{}, HostInfo{}))
	if err != nil {
		t.Fatal(err)
	}
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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("BuildChain() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildChain() error = %v", err)
			}
			if len(tt.refs) == 0 {
				if chain != nil {
					t.Fatal("empty refs should give nil chain")
				}
				return
			}
			if len(chain.Steps) != len(tt.steps) {
				t.Fatalf("steps = %d, want %d", len(chain.Steps), len(tt.steps))
			}
			for i, order := range tt.steps {
				if chain.Steps[i].Order != order {
					t.Fatalf("step %d order = %d, want %d", i, chain.Steps[i].Order, order)
				}
			}
			if chain.Hash == "" || chain.Len() != len(tt.refs) {
				t.Fatalf("chain = %+v", chain)
			}
		})
	}
}

func TestChainHashChangesWithConfigStepAndFailMode(t *testing.T) {
	build := func(config string, step int, mode FailMode) string {
		inst := newTestInstance(&fakePlugin{name: "p", schema: []pluginapi.Field{{Key: "v", Input: pluginapi.InputText}}}, InstanceSpec{Config: json.RawMessage(config), FailMode: mode})
		chain, err := BuildChain(pluginapi.KindPrompt, []Ref{{inst, step}})
		if err != nil {
			t.Fatal(err)
		}
		return chain.Hash
	}
	base := build(`{"v":"a"}`, 10, "")
	if base != build(`{"v":"a"}`, 10, "") {
		t.Fatal("hash not stable")
	}
	for name, other := range map[string]string{
		"config":   build(`{"v":"b"}`, 10, ""),
		"step":     build(`{"v":"a"}`, 20, ""),
		"failmode": build(`{"v":"a"}`, 10, FailOpen),
	} {
		if other == base {
			t.Fatalf("hash unchanged for %s", name)
		}
	}
	chains := &Chains{Prompt: &Chain{Hash: "p", Steps: []Step{{Order: 1}}}}
	if got := chains.Hashes(); len(got) != 1 || got["prompt"] != "p" {
		t.Fatalf("Hashes() = %v", got)
	}
	if chains.Empty() || chains.PromptHash() != "p" {
		t.Fatal("Chains helpers wrong")
	}
}

func TestComputeChainHashFormat(t *testing.T) {
	if ComputeChainHash(nil) != "" {
		t.Fatal("empty hash should be empty")
	}
	a := ComputeChainHash([]RuleDescriptor{{Name: "a", Type: "t", Order: 1, Mode: "closed", Content: "x"}, {Name: "b", Type: "t", Order: 2, Mode: "closed", Content: "y"}})
	b := ComputeChainHash([]RuleDescriptor{{Name: "b", Type: "t", Order: 2, Mode: "closed", Content: "y"}, {Name: "a", Type: "t", Order: 1, Mode: "closed", Content: "x"}})
	if a != b || len(a) != 64 {
		t.Fatalf("hash order-dependent or wrong length: %q %q", a, b)
	}
}

func TestInstanceStreamPolicyDefaultsToObserve(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "s"}, InstanceSpec{})
	if inst.StreamPolicy().Mode != pluginapi.StreamObserve {
		t.Fatalf("mode = %q", inst.StreamPolicy().Mode)
	}
	inst.Timeout = time.Second
	if inst.Timeout != time.Second {
		t.Fatal("timeout not kept")
	}
}

type panickingPolicy struct{ *fakePlugin }

func (panickingPolicy) StreamPolicy() pluginapi.StreamPolicy { panic("policy boom") }

func TestInstanceStreamPolicyRecoversPanic(t *testing.T) {
	inst := &Instance{Name: "s", Type: "s", Plugin: panickingPolicy{&fakePlugin{name: "s"}}, Kinds: []pluginapi.Kind{pluginapi.KindStream}}
	if got := inst.StreamPolicy().Mode; got != pluginapi.StreamObserve {
		t.Fatalf("mode after panic = %q, want observe", got)
	}
}

func TestChainsCacheHash(t *testing.T) {
	promptOnly := &Chains{Prompt: &Chain{Hash: "p", Steps: []Step{{Order: 1}}}}
	if promptOnly.CacheHash() != "p" {
		t.Fatalf("prompt-only cache hash = %q, want the prompt hash", promptOnly.CacheHash())
	}
	withResponse := &Chains{Prompt: promptOnly.Prompt, Response: &Chain{Hash: "r", Steps: []Step{{Order: 1}}}}
	other := &Chains{Prompt: promptOnly.Prompt, Response: &Chain{Hash: "r2", Steps: []Step{{Order: 1}}}}
	if withResponse.CacheHash() == "p" || withResponse.CacheHash() == other.CacheHash() || len(withResponse.CacheHash()) != 64 {
		t.Fatalf("cache hash with response chain = %q / %q", withResponse.CacheHash(), other.CacheHash())
	}
	if (&Chains{}).CacheHash() != "" || (*Chains)(nil).CacheHash() != "" {
		t.Fatal("empty chains must hash to empty")
	}
}
