package providers

import (
	"math"
	"strconv"
	"strings"
)

// Helpers for the metadata a provider reports about its own models
// ("discovered" metadata). Capability keys follow the model catalog's
// vocabulary (the ai-model-list schema), so a provider's report and the
// catalog entry describe one feature under one key and merge field by field
// instead of listing the same feature twice under different names.

// capabilityAliases maps the names providers use for a feature onto the
// catalog's capability key.
var capabilityAliases = map[string]string{
	"tools":                     "function_calling",
	"tool_use":                  "function_calling",
	"tool_calling":              "function_calling",
	"functions":                 "function_calling",
	"function_calling":          "function_calling",
	"parallel_tool_calls":       "parallel_function_calling",
	"parallel_function_calling": "parallel_function_calling",
	"tool_choice":               "tool_choice",
	"json_mode":                 "json_mode",
	"json_object":               "json_mode",
	"response_format":           "json_mode",
	"json_schema":               "structured_output",
	"structured_outputs":        "structured_output",
	"structured_output":         "structured_output",
	"response_schema":           "response_schema",
	"reasoning":                 "reasoning",
	"thinking":                  "reasoning",
	"extended_thinking":         "reasoning",
	"vision":                    "vision",
	"image_input":               "vision",
	"image":                     "vision",
	"audio":                     "audio_input",
	"audio_input":               "audio_input",
	"speech":                    "audio_output",
	"audio_output":              "audio_output",
	"video":                     "video_input",
	"video_input":               "video_input",
	"pdf":                       "pdf_input",
	"pdf_input":                 "pdf_input",
	"web_search":                "web_search",
	"web_search_options":        "web_search",
	"prompt_caching":            "prompt_caching",
	"caching":                   "prompt_caching",
	"streaming":                 "streaming",
	"computer_use":              "computer_use",
	"system_messages":           "system_messages",
	"assistant_prefill":         "assistant_prefill",
}

// CapabilityKey returns the catalog key for a provider's feature name. Names
// the catalog has no key for come back lowercased and trimmed, so a
// provider-specific flag (confidential_compute) still surfaces under its own
// name. Blank names return "".
func CapabilityKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return ""
	}
	if canonical, ok := capabilityAliases[key]; ok {
		return canonical
	}
	return key
}

// SetCapability records a feature under its catalog key, allocating the map
// on first use. A blank name is ignored, and a feature already recorded as
// supported is not flipped to unsupported by a second alias that says
// otherwise.
func SetCapability(capabilities map[string]bool, name string, supported bool) map[string]bool {
	key := CapabilityKey(name)
	if key == "" {
		return capabilities
	}
	if capabilities == nil {
		capabilities = make(map[string]bool)
	}
	if current, ok := capabilities[key]; ok && current && !supported {
		return capabilities
	}
	capabilities[key] = supported
	return capabilities
}

// CapabilitiesFromFeatures marks every listed feature as supported.
func CapabilitiesFromFeatures(capabilities map[string]bool, features []string) map[string]bool {
	for _, feature := range features {
		capabilities = SetCapability(capabilities, feature, true)
	}
	return capabilities
}

// CapabilitiesFromInputModalities marks the input types a model accepts
// besides text. Text itself is not a capability.
func CapabilitiesFromInputModalities(capabilities map[string]bool, modalities []string) map[string]bool {
	for _, modality := range modalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			capabilities = SetCapability(capabilities, "vision", true)
		case "audio":
			capabilities = SetCapability(capabilities, "audio_input", true)
		case "video":
			capabilities = SetCapability(capabilities, "video_input", true)
		case "file", "pdf", "document":
			capabilities = SetCapability(capabilities, "pdf_input", true)
		}
	}
	return capabilities
}

// PerTokenRateToMtok parses a per-token USD rate (the decimal string several
// listings use) and scales it to per million tokens. Rates that are
// unparseable, negative, or non-finite report no price rather than a wrong
// one: ParseFloat accepts "NaN" and "Inf", and scaling a huge rate can
// overflow to infinity, either of which would corrupt every downstream price
// comparison and cost calculation.
func PerTokenRateToMtok(rate string) (float64, bool) {
	perToken, err := strconv.ParseFloat(strings.TrimSpace(rate), 64)
	if err != nil || perToken < 0 || math.IsNaN(perToken) || math.IsInf(perToken, 0) {
		return 0, false
	}
	scaled := perToken * 1_000_000
	if math.IsInf(scaled, 0) {
		return 0, false
	}
	return scaled, true
}
