package run

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepeatDemoModeWarnings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		repeatDemoModeWarnings(ctx, time.Millisecond, func() {
			if count.Add(1) == 2 {
				close(done)
			}
		})
	}()

	select {
	case <-done:
		cancel()
	case <-time.After(time.Second):
		t.Fatalf("received %d warnings, want at least 2", count.Load())
	}
}

func TestDemoModeFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset defaults off"},
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "false", value: "false"},
		{name: "invalid", value: "sometimes", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envDemoMode, tt.value)
			got, err := demoModeFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("demoModeFromEnv() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("demoModeFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}
