package config

import (
	"strings"
	"testing"
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
			if got != tt.want {
				t.Fatalf("ResolveImageBodyScope(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got.Valid() != tt.valid {
				t.Fatalf("Valid() = %v, want %v", got.Valid(), tt.valid)
			}
			if !tt.valid {
				return
			}
			if got.Inputs() != tt.inputs || got.Outputs() != tt.outs {
				t.Fatalf("Inputs/Outputs = %v/%v, want %v/%v", got.Inputs(), got.Outputs(), tt.inputs, tt.outs)
			}
		})
	}
}

func TestLoadImageBodyLoggingEnv(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.Logging.LogImageBodies {
			t.Fatal("log_image_bodies should default to false")
		}
		if got := result.Config.Logging.LogImageBodiesScope; got != ImageBodyScopeAll {
			t.Fatalf("log_image_bodies_scope default = %q, want all", got)
		}

		t.Setenv("LOGGING_LOG_IMAGE_BODIES", "true")
		t.Setenv("LOGGING_LOG_IMAGE_BODIES_SCOPE", "Output")
		result, err = Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if !result.Config.Logging.LogImageBodies || result.Config.Logging.LogImageBodiesScope != ImageBodyScopeOutput {
			t.Fatalf("logging = %+v, want image bodies on with output scope", result.Config.Logging)
		}

		t.Setenv("LOGGING_LOG_IMAGE_BODIES_SCOPE", "pixels")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "log_image_bodies_scope") {
			t.Fatalf("Load() error = %v, want invalid scope error", err)
		}
	})
}
