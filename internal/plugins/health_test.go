package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

// healthPlugin is a fakePlugin with a Health probe. The probe function is
// guarded because an abandoned probe may still be running when a test
// swaps it.
type healthPlugin struct {
	fakePlugin
	mu     sync.Mutex
	health func(ctx context.Context) error
}

func (p *healthPlugin) Health(ctx context.Context) error {
	p.mu.Lock()
	probe := p.health
	p.mu.Unlock()
	return probe(ctx)
}

func (p *healthPlugin) setHealth(probe func(ctx context.Context) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health = probe
}

func newHealthInstance(t *testing.T, health func(ctx context.Context) error, spec InstanceSpec) *Instance {
	t.Helper()
	p := &healthPlugin{name: "checker", kinds: []pluginapi.Kind{pluginapi.KindPrompt}, health: health}
	if spec.Name == "" {
		spec.Name = "checker"
	}
	inst, err := NewInstance(context.Background(), newEntry(p), spec, NewHost(HostDeps{}, HostInfo{PluginName: p.name, InstanceName: spec.Name}))
	require.NoError(t, err)

	return inst
}

func TestInstanceHealthWithoutChecker(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "plain", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}, InstanceSpec{})
	require.False(t, inst.Checks())

	got := inst.CheckHealth(context.Background())
	require.Equal(t, HealthOK, got.Status)
	require.False(t, got.Degraded())
	require.True(t, got.CheckedAt.IsZero(), "CheckHealth() = %+v, want ok without a probe time", got)
	require.Equal(t, HealthOK, inst.Health().Status, "Health() = %+v, want ok", inst.Health())

	var nilInst *Instance
	require.False(t, nilInst.Checks())
	require.Equal(t, HealthOK, nilInst.Health().Status)
}

func TestInstanceCheckHealth(t *testing.T) {
	tests := []struct {
		name    string
		health  func(ctx context.Context) error
		timeout time.Duration
		want    string
		wantErr string
	}{
		{name: "ok", health: func(context.Context) error { return nil }, want: HealthOK},
		{name: "error", health: func(context.Context) error { return errors.New("analyzer unreachable") }, want: HealthDegraded, wantErr: "analyzer unreachable"},
		{name: "panic", health: func(context.Context) error { panic("boom") }, want: HealthDegraded, wantErr: "panicked"},
		{name: "instance timeout", timeout: 20 * time.Millisecond, health: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}, want: HealthDegraded, wantErr: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := newHealthInstance(t, tt.health, InstanceSpec{Timeout: tt.timeout})
			require.True(t, inst.Checks())
			require.Equal(t, HealthOK, inst.Health().Status, "Health() before a probe = %+v, want ok", inst.Health())

			before := time.Now()
			got := inst.CheckHealth(context.Background())
			require.Equal(t, tt.want, got.Status)
			require.Contains(t, got.Error, tt.wantErr)
			require.False(t, got.CheckedAt.Before(before), "CheckedAt %v predates the probe", got.CheckedAt)
			require.Equal(t, got, inst.Health())
		})
	}
}

func TestInstanceCheckHealthUsesProbeDeadline(t *testing.T) {
	old := healthTimeout
	healthTimeout = 20 * time.Millisecond
	t.Cleanup(func() { healthTimeout = old })
	inst := newHealthInstance(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, InstanceSpec{})
	got := inst.CheckHealth(context.Background())
	require.True(t, got.Degraded())
	require.Contains(t, got.Error, "abandoned", "CheckHealth() = %+v, want degraded by the probe deadline", got)

	// A later successful probe recovers.
	inst.Plugin.(*healthPlugin).setHealth(func(context.Context) error { return nil })
	got = inst.CheckHealth(context.Background())
	require.False(t, got.Degraded())
}

func TestInstanceCheckHealthBoundsErrorText(t *testing.T) {
	long := strings.Repeat("é", 300)
	inst := newHealthInstance(t, func(context.Context) error { return errors.New(long) }, InstanceSpec{})
	got := inst.CheckHealth(context.Background())
	require.True(t, got.Degraded())
	require.LessOrEqual(t, len(got.Error), maxHealthErrorLen+len("…"))
	require.True(t, strings.HasSuffix(got.Error, "…"))
	require.True(t, utf8.ValidString(got.Error), "Error = %q (%d bytes), want a valid string bounded to %d bytes plus an ellipsis", got.Error, len(got.Error), maxHealthErrorLen)
}
