package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
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
	if err != nil {
		t.Fatalf("NewInstance() error = %v", err)
	}
	return inst
}

func TestInstanceHealthWithoutChecker(t *testing.T) {
	inst := newTestInstance(&fakePlugin{name: "plain", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}, InstanceSpec{})
	if inst.Checks() {
		t.Fatal("a plugin without Health must not be a checker")
	}
	got := inst.CheckHealth(context.Background())
	if got.Status != HealthOK || got.Degraded() || !got.CheckedAt.IsZero() {
		t.Fatalf("CheckHealth() = %+v, want ok without a probe time", got)
	}
	if inst.Health().Status != HealthOK {
		t.Fatalf("Health() = %+v, want ok", inst.Health())
	}
	var nilInst *Instance
	if nilInst.Checks() || nilInst.Health().Status != HealthOK {
		t.Fatal("a nil instance must read as ok")
	}
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
			if !inst.Checks() {
				t.Fatal("plugin with Health must be a checker")
			}
			if inst.Health().Status != HealthOK {
				t.Fatalf("Health() before a probe = %+v, want ok", inst.Health())
			}
			before := time.Now()
			got := inst.CheckHealth(context.Background())
			if got.Status != tt.want || !strings.Contains(got.Error, tt.wantErr) {
				t.Fatalf("CheckHealth() = %+v, want status %q with error containing %q", got, tt.want, tt.wantErr)
			}
			if got.CheckedAt.Before(before) {
				t.Fatalf("CheckedAt %v predates the probe", got.CheckedAt)
			}
			if inst.Health() != got {
				t.Fatalf("Health() = %+v, want the last probe %+v", inst.Health(), got)
			}
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
	if !got.Degraded() || !strings.Contains(got.Error, "abandoned") {
		t.Fatalf("CheckHealth() = %+v, want degraded by the probe deadline", got)
	}
	// A later successful probe recovers.
	inst.Plugin.(*healthPlugin).setHealth(func(context.Context) error { return nil })
	if got := inst.CheckHealth(context.Background()); got.Degraded() {
		t.Fatalf("CheckHealth() after recovery = %+v", got)
	}
}
