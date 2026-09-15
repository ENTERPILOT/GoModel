package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageBodyScope(t *testing.T) {
	tests := []struct {
		raw          ImageBodyScope
		want         ImageBodyScope
		valid        bool
		inputs, outs bool
	}{
		{raw: "", want: ImageBodyScopeAll, valid: true, inputs: true, outs: true},
		{raw: " All ", want: ImageBodyScopeAll, valid: true, inputs: true, outs: true},
		{raw: "input", want: ImageBodyScopeInput, valid: true, inputs: true, outs: false},
		{raw: "OUTPUT", want: ImageBodyScopeOutput, valid: true, inputs: false, outs: true},
		{raw: "both", want: "both", valid: false},
	}
	for _, tt := range tests {
		t.Run(string(tt.raw), func(t *testing.T) {
			got := ResolveImageBodyScope(tt.raw)
			require.Equal(t, tt.want, got, "ResolveImageBodyScope(%q)", tt.raw)
			require.Equal(t, tt.valid, got.Valid())

			if !tt.valid {
				return
			}
			require.Equal(t, tt.inputs, got.Inputs())
			require.Equal(t, tt.outs, got.Outputs())
		})
	}
}

func TestLoadImageBodyLoggingEnv(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.False(t, result.Config.Logging.LogImageBodies)
		got := result.Config.Logging.LogImageBodiesScope
		require.Equal(t, ImageBodyScopeAll, got)

		t.Setenv("LOGGING_LOG_IMAGE_BODIES", "true")
		t.Setenv("LOGGING_LOG_IMAGE_BODIES_SCOPE", "Output")
		result, err = Load()
		require.NoError(t, err)
		require.True(t, result.Config.Logging.LogImageBodies)
		require.Equal(t, ImageBodyScopeOutput, result.Config.Logging.LogImageBodiesScope, "logging = %+v, want image bodies on with output scope", result.Config.Logging)

		t.Setenv("LOGGING_LOG_IMAGE_BODIES_SCOPE", "pixels")
		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "log_image_bodies_scope")
	})
}
