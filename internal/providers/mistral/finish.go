package mistral

import (
	"bytes"

	"github.com/goccy/go-json"
)

// Mistral reports a completion cut off by the model's context window as
// finish_reason "model_length" (alongside the OpenAI-style "length" for
// max_tokens). Chat clients only know the OpenAI vocabulary, so the value is
// mapped to "length".
const (
	finishReasonModelLength = "model_length"
	finishReasonLength      = "length"
)

var modelLengthMarker = []byte(finishReasonModelLength)

// mapFinishReasons rewrites choices[].finish_reason "model_length" to
// "length" in a chat completion payload (buffered or single SSE chunk). It
// returns the original data untouched when the marker is absent, and reports
// whether a rewrite happened.
func mapFinishReasons(data []byte) ([]byte, bool) {
	if !bytes.Contains(data, modelLengthMarker) {
		return data, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return data, false
	}
	choicesRaw, ok := top["choices"]
	if !ok {
		return data, false
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return data, false
	}
	changed := false
	for _, choice := range choices {
		if rawString(choice["finish_reason"]) != finishReasonModelLength {
			continue
		}
		choice["finish_reason"] = json.RawMessage(`"` + finishReasonLength + `"`)
		changed = true
	}
	if !changed {
		return data, false
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return data, false
	}
	top["choices"] = encodedChoices
	out, err := json.Marshal(top)
	if err != nil {
		return data, false
	}
	return out, true
}
