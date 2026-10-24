package httpclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStreamIdleTimeout(t *testing.T) {
	tests := []struct {
		name       string
		configured *int
		env        string
		want       time.Duration
	}{
		{name: "default", want: DefaultStreamIdleTimeout},
		{name: "config file", configured: new(60), want: time.Minute},
		{name: "config file disables", configured: new(0), want: 0},
		{name: "env seconds override config", configured: new(60), env: "30", want: 30 * time.Second},
		{name: "env duration", env: "2m", want: 2 * time.Minute},
		{name: "env disables", env: "0", want: 0},
		{name: "invalid env keeps config", configured: new(45), env: "soon", want: 45 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				streamIdleTimeoutConfigured.Store(false)
				configuredStreamIdleTimeoutSeconds.Store(0)
			})
			t.Setenv("HTTP_STREAM_IDLE_TIMEOUT", tt.env)
			if tt.configured != nil {
				SetConfiguredStreamIdleTimeout(*tt.configured)
			}
			assert.Equal(t, tt.want, StreamIdleTimeout())
		})
	}
}
