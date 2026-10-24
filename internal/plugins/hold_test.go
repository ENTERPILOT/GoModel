package plugins

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

func TestClosedInstanceRefusesCalls(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "p"}, InstanceSpec{})
	err := inst.Close(context.Background())
	require.NoError(t, err)
	err = inst.Close(context.Background())
	require.NoError(t, err)

	called := false
	_, err = Call(context.Background(), inst, func(context.Context) (pluginapi.Decision, error) {
		called = true
		return pluginapi.Allow(), nil
	})
	require.ErrorIs(t, err, ErrInstanceClosed)
	require.False(t, called)
}

func TestChainsAcquireHoldsEachInstanceOnce(t *testing.T) {
	a := newTestInstance(&fakePlugin{name: "a"}, InstanceSpec{})
	b := newTestInstance(&fakePlugin{name: "b"}, InstanceSpec{})
	chains := &Chains{
		Prompt:   &Chain{Steps: []Step{{Instances: []*Instance{a, b}}}},
		Response: &Chain{Steps: []Step{{Instances: []*Instance{a}}}},
	}
	chains.Acquire()
	require.True(t, a.Held())
	require.True(t, b.Held())
	require.Equal(t, int64(1), a.refs.Load())
	require.Equal(t, int64(1), b.refs.Load())

	chains.Release()
	require.False(t, a.Held())
	require.False(t, b.Held())

	var none *Chains
	none.Acquire()
	none.Release()
}
