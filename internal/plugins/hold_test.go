package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestClosedInstanceRefusesCalls(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "p"}, InstanceSpec{})
	if err := inst.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v, want nil (idempotent)", err)
	}
	called := false
	_, err := Call(context.Background(), inst, func(context.Context) (pluginapi.Decision, error) {
		called = true
		return pluginapi.Allow(), nil
	})
	if !errors.Is(err, ErrInstanceClosed) || called {
		t.Fatalf("Call on closed instance: err = %v, called = %v; want ErrInstanceClosed without calling", err, called)
	}
}

func TestChainsAcquireHoldsEachInstanceOnce(t *testing.T) {
	a := newTestInstance(&fakePlugin{name: "a"}, InstanceSpec{})
	b := newTestInstance(&fakePlugin{name: "b"}, InstanceSpec{})
	chains := &Chains{
		Prompt:   &Chain{Steps: []Step{{Instances: []*Instance{a, b}}}},
		Response: &Chain{Steps: []Step{{Instances: []*Instance{a}}}},
	}
	chains.Acquire()
	if !a.Held() || !b.Held() || a.refs.Load() != 1 || b.refs.Load() != 1 {
		t.Fatalf("refs after Acquire: a=%d b=%d, want 1 each", a.refs.Load(), b.refs.Load())
	}
	chains.Release()
	if a.Held() || b.Held() {
		t.Fatal("instances still held after Release")
	}
	var none *Chains
	none.Acquire()
	none.Release()
}
