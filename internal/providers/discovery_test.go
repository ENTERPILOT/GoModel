package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCapabilityKey(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "tools", want: "function_calling"},
		{name: " Tool_Use ", want: "function_calling"},
		{name: "structured_outputs", want: "structured_output"},
		{name: "response_format", want: "json_mode"},
		{name: "thinking", want: "reasoning"},
		{name: "image_input", want: "vision"},
		{name: "video", want: "video_input"},
		{name: "speech", want: "audio_output"},
		{name: "confidential_compute", want: "confidential_compute"},
		{name: "", want: ""},
		{name: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CapabilityKey(tt.name))
		})
	}
}

func TestSetCapability(t *testing.T) {
	var capabilities map[string]bool
	capabilities = SetCapability(capabilities, "", true)
	assert.Nil(t, capabilities, "blank names allocate nothing")

	capabilities = SetCapability(capabilities, "tools", true)
	capabilities = SetCapability(capabilities, "function_calling", false)
	assert.Equal(t, map[string]bool{"function_calling": true}, capabilities, "a supported feature is not flipped by a second alias")

	capabilities = SetCapability(capabilities, "vision", false)
	capabilities = SetCapability(capabilities, "image", true)
	assert.True(t, capabilities["vision"], "an unsupported feature can be confirmed later")
}

func TestCapabilitiesFromFeaturesAndModalities(t *testing.T) {
	capabilities := CapabilitiesFromFeatures(nil, []string{"tools", "json_mode", "structured_outputs", "reasoning", ""})
	capabilities = CapabilitiesFromInputModalities(capabilities, []string{"text", "image", "audio", "video", "file"})
	assert.Equal(t, map[string]bool{
		"function_calling":  true,
		"json_mode":         true,
		"structured_output": true,
		"reasoning":         true,
		"vision":            true,
		"audio_input":       true,
		"video_input":       true,
		"pdf_input":         true,
	}, capabilities)
	assert.Nil(t, CapabilitiesFromInputModalities(nil, []string{"text"}), "text alone is no capability")
}

func TestPerTokenRateToMtok(t *testing.T) {
	tests := []struct {
		rate string
		want float64
		ok   bool
	}{
		{rate: "0.00000015", want: 0.15, ok: true},
		{rate: "0", want: 0, ok: true},
		{rate: "-1", ok: false},
		{rate: "NaN", ok: false},
		{rate: "Inf", ok: false},
		{rate: "1e308", ok: false},
		{rate: "", ok: false},
		{rate: "abc", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.rate, func(t *testing.T) {
			got, ok := PerTokenRateToMtok(tt.rate)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.InDelta(t, tt.want, got, 1e-12)
			}
		})
	}
}
