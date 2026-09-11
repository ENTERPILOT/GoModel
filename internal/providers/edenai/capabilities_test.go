package edenai

import (
	"testing"
)

// TestCapabilities_LiveSchemaOnly pins the one capability schema Eden actually
// publishes, checked against the live /v3/models catalog: a `capabilities`
// object holding `input_modalities`, `output_modalities`, and a growing set of
// `supports_*` booleans.
//
// Eden does not publish bare capability flags such as "pdf", "tool_calling",
// or "web_search" alongside those — the equivalents are supports_pdf_input,
// supports_tools/supports_function_calling, and supports_web_search — so this
// provider deliberately reads only the prefixed form. Accepting unprefixed
// names as well would mean advertising capabilities on the strength of a
// schema Eden does not serve.
func TestCapabilities_LiveSchemaOnly(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"capabilities": {
			"input_modalities": ["text", "image"],
			"output_modalities": ["text"],
			"supports_reasoning": true,
			"supports_web_search": true,
			"supports_function_calling": true,
			"supports_prompt_caching": true,
			"supports_tool_choice": false,
			"supports_computer_use": false
		}
	}]}`)

	capabilities := model.Metadata.Capabilities
	// The supports_ prefix is stripped so the names read the way other
	// providers report them.
	for _, name := range []string{"reasoning", "web_search", "function_calling", "prompt_caching"} {
		if !capabilities[name] {
			t.Errorf("capability %q = false, want true", name)
		}
	}
	// An image input modality is reported as vision, the gateway's name for it.
	if !capabilities["vision"] {
		t.Error(`capability "vision" = false, want true for an image input modality`)
	}
	// A flag Eden reports as false must not be advertised at all.
	for _, name := range []string{"tool_choice", "computer_use"} {
		if _, present := capabilities[name]; present {
			t.Errorf("capability %q is present, want absent: Eden reported it false", name)
		}
	}
	// The prefixed keys must not leak through under their raw Eden names.
	for _, name := range []string{"supports_reasoning", "supports_web_search", "input_modalities", "output_modalities"} {
		if _, present := capabilities[name]; present {
			t.Errorf("capability %q is present, want the Eden key name not to leak", name)
		}
	}
}

// TestCapabilities_RareSupportsFlagsFlowThrough asserts the long tail of
// supports_* flags Eden publishes on only a handful of models is picked up
// without a source change here, and lands on the gateway's own capability
// names where one already exists.
//
// supports_vision and supports_tools matter most: "vision" and "tools" are
// names other providers already report, so stripping the prefix is what keeps
// Eden's vocabulary aligned rather than parallel.
func TestCapabilities_RareSupportsFlagsFlowThrough(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"capabilities": {
			"output_modalities": ["text"],
			"supports_vision": true,
			"supports_tools": true,
			"supports_pdf_input": true,
			"supports_audio_input": true,
			"supports_structured_output": true,
			"supports_responses_api": true
		}
	}]}`)

	for _, name := range []string{
		"vision",
		"tools",
		"pdf_input",
		"audio_input",
		"structured_output",
		"responses_api",
	} {
		if !model.Metadata.Capabilities[name] {
			t.Errorf("capability %q = false, want true", name)
		}
	}
}

// TestCapabilities_ObjectValuedReasoningIsNotAFlag guards a real shape in the
// live catalog: most Eden entries carry a `reasoning` member that is an object
// describing reasoning options, sitting next to the separate
// `supports_reasoning` boolean.
//
// It must not become a capability. It has no supports_ prefix and is not a
// bool, so both screens in capabilities() have to hold — otherwise every model
// with reasoning metadata would claim a "reasoning" capability regardless of
// what supports_reasoning actually says.
func TestCapabilities_ObjectValuedReasoningIsNotAFlag(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"capabilities": {
			"output_modalities": ["text"],
			"supports_reasoning": false,
			"reasoning": {"effort": ["low", "medium", "high"]},
			"supports_web_search": true
		}
	}]}`)

	capabilities := model.Metadata.Capabilities
	if _, present := capabilities["reasoning"]; present {
		t.Error(`capability "reasoning" is present, but Eden reported supports_reasoning: false; the object-valued "reasoning" member must not be read as a flag`)
	}
	if !capabilities["web_search"] {
		t.Error(`capability "web_search" = false, want true: a sibling object member must not stop the real flags being read`)
	}
}

// TestCapabilities_NonBooleanSupportsValuesIgnored asserts a supports_* member
// Eden ever publishes as something other than a boolean is skipped rather than
// coerced into a true.
func TestCapabilities_NonBooleanSupportsValuesIgnored(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"capabilities": {
			"output_modalities": ["text"],
			"supports_web_search": "yes",
			"supports_tool_choice": 1,
			"supports_prompt_caching": null,
			"supports_reasoning": true
		}
	}]}`)

	capabilities := model.Metadata.Capabilities
	for _, name := range []string{"web_search", "tool_choice", "prompt_caching"} {
		if _, present := capabilities[name]; present {
			t.Errorf("capability %q is present, want absent: Eden did not report a boolean", name)
		}
	}
	if !capabilities["reasoning"] {
		t.Error(`capability "reasoning" = false, want true`)
	}
}

// TestCapabilities_BareSupportsPrefixIgnored asserts a key that is exactly the
// prefix contributes no empty-named capability.
func TestCapabilities_BareSupportsPrefixIgnored(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"openai/gpt-4","object":"model",
		"capabilities": {"output_modalities": ["text"], "supports_": true}
	}]}`)

	if _, present := model.Metadata.Capabilities[""]; present {
		t.Error(`an empty-named capability was recorded for the bare "supports_" key`)
	}
}

// TestCapabilities_InputModalityMapping pins the input modalities the live
// catalog actually contains (text, image, file, video, audio) onto the
// gateway's capability names. "file" has no gateway capability and is skipped
// rather than guessed at.
func TestCapabilities_InputModalityMapping(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"google/gemini-3.8-flash","object":"model",
		"capabilities": {
			"input_modalities": ["text", "image", "video", "file", "audio"],
			"output_modalities": ["text"]
		}
	}]}`)

	capabilities := model.Metadata.Capabilities
	for _, name := range []string{"vision", "video", "audio"} {
		if !capabilities[name] {
			t.Errorf("capability %q = false, want true", name)
		}
	}
	for _, name := range []string{"file", "text"} {
		if _, present := capabilities[name]; present {
			t.Errorf("capability %q is present; modalities with no gateway capability must be skipped", name)
		}
	}
}

// TestCapabilities_VideoInputIsNotVideoOutput is the distinction finding 7
// turned on. In the live catalog `video` appears only as an *input* modality
// (143 of 1049 models) and never as an output one, so a video-understanding
// model is an ordinary chat model that happens to accept video.
//
// It must keep its chat modes and stay advertised; only a model whose *output*
// is video is unservable here.
func TestCapabilities_VideoInputIsNotVideoOutput(t *testing.T) {
	model := firstModel(t, `{"object":"list","data":[{
		"id":"qwen/qwen3.8-max-0902","object":"model",
		"capabilities": {
			"input_modalities": ["text", "image", "video"],
			"output_modalities": ["text"]
		}
	}]}`)

	if !model.Metadata.Capabilities["video"] {
		t.Error(`capability "video" = false, want true for a video input modality`)
	}
	modes := model.Metadata.Modes
	if len(modes) != 2 || modes[0] != "chat" || modes[1] != "responses" {
		t.Errorf("modes = %v, want [chat responses]: video input must not produce a video mode", modes)
	}
}
