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

// TestLoadTrustedProxyCIDRs covers the operator-facing shape of the setting:
// off unless configured, normalized when set, and a startup error on a value
// that cannot describe a network.
func TestLoadTrustedProxyCIDRs(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.Empty(t, result.Config.Logging.TrustedProxyCIDRs, "forwarding headers must be ignored unless the operator lists proxy networks")

		t.Setenv("LOGGING_TRUSTED_PROXY_CIDRS", " 10.0.0.0/8 , 127.0.0.1, 10.0.0.0/8 ,")
		result, err = Load()
		require.NoError(t, err)
		require.Equal(t, []string{"10.0.0.0/8", "127.0.0.1/32"}, result.Config.Logging.TrustedProxyCIDRs)

		t.Setenv("LOGGING_TRUSTED_PROXY_CIDRS", "2001:db8::5")
		result, err = Load()
		require.NoError(t, err)
		require.Equal(t, []string{"2001:db8::5/128"}, result.Config.Logging.TrustedProxyCIDRs)

		t.Setenv("LOGGING_TRUSTED_PROXY_CIDRS", "10.0.0.0/33")
		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "trusted_proxy_cidrs")
	})
}

// TestNormalizeTrustedProxyCIDRsMappedIPv6 pins the handling of IPv4-mapped
// networks, which are the ones an operator can write believing a proxy network
// is trusted while it silently matches nothing.
func TestNormalizeTrustedProxyCIDRsMappedIPv6(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{
			name: "mapped prefix inside the embedded address becomes the IPv4 network",
			in:   []string{"::ffff:10.0.0.0/120"},
			want: []string{"10.0.0.0/24"},
		},
		{
			name: "mapped /96 is the whole IPv4 space",
			in:   []string{"::ffff:0:0/96"},
			want: []string{"0.0.0.0/0"},
		},
		{
			name:    "mapped prefix below /96 reaches past the embedded address",
			in:      []string{"::ffff:10.0.0.0/8"},
			wantErr: "shorter than /96",
		},
		{
			name: "plain IPv4 and IPv6 networks are untouched",
			in:   []string{"10.0.0.0/8", "2001:db8::/32", "::1"},
			want: []string{"10.0.0.0/8", "2001:db8::/32", "::1/128"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &LogConfig{TrustedProxyCIDRs: tt.in}
			err := NormalizeTrustedProxyCIDRs(cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.TrustedProxyCIDRs)
		})
	}
}

// TestNormalizeTrustedProxyCIDRsDegenerateInput covers the shapes a config file
// can hold that environment parsing would have filtered out already: no
// configuration at all, and entries that carry no value.
func TestNormalizeTrustedProxyCIDRsDegenerateInput(t *testing.T) {
	require.NoError(t, NormalizeTrustedProxyCIDRs(nil))

	cfg := &LogConfig{TrustedProxyCIDRs: []string{" ", ""}}
	require.NoError(t, NormalizeTrustedProxyCIDRs(cfg))
	require.Empty(t, cfg.TrustedProxyCIDRs)
}
