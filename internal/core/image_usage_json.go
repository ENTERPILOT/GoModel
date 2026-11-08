package core

import "github.com/goccy/go-json"

// imageUsageKnownFields are the members ImageUsage decodes into typed fields,
// under either the Images (input_/output_) or the Chat (prompt_/completion_)
// naming.
var imageUsageKnownFields = map[string]struct{}{
	"input_tokens":              {},
	"output_tokens":             {},
	"total_tokens":              {},
	"input_tokens_details":      {},
	"output_tokens_details":     {},
	"prompt_tokens":             {},
	"completion_tokens":         {},
	"prompt_tokens_details":     {},
	"completion_tokens_details": {},
}

// UnmarshalJSON accepts both usage namings. The Images naming wins when a
// provider sends both; unknown members are collected into RawUsage.
func (u *ImageUsage) UnmarshalJSON(data []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	var decoded ImageUsage
	if err := decodeImageUsageField(payload, &decoded.InputTokens, "input_tokens", "prompt_tokens"); err != nil {
		return err
	}
	if err := decodeImageUsageField(payload, &decoded.OutputTokens, "output_tokens", "completion_tokens"); err != nil {
		return err
	}
	if err := decodeImageUsageField(payload, &decoded.TotalTokens, "total_tokens"); err != nil {
		return err
	}
	if err := decodeImageUsageField(payload, &decoded.InputTokensDetails, "input_tokens_details", "prompt_tokens_details"); err != nil {
		return err
	}
	if err := decodeImageUsageField(payload, &decoded.OutputTokensDetails, "output_tokens_details", "completion_tokens_details"); err != nil {
		return err
	}

	for key, raw := range payload {
		if _, known := imageUsageKnownFields[key]; known {
			continue
		}
		if err := mergeRawUsageField(&decoded.RawUsage, key, raw); err != nil {
			return err
		}
	}

	*u = decoded
	return nil
}

// decodeImageUsageField decodes the first present, non-null key into dst.
func decodeImageUsageField(payload map[string]json.RawMessage, dst any, keys ...string) error {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || string(raw) == "null" {
			continue
		}
		return json.Unmarshal(raw, dst)
	}
	return nil
}

// MarshalJSON emits the OpenAI Images usage shape, followed by any provider
// extras from RawUsage that do not collide with a typed member.
func (u ImageUsage) MarshalJSON() ([]byte, error) {
	type alias ImageUsage
	if len(u.RawUsage) == 0 {
		return json.Marshal(alias(u))
	}
	payload := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"total_tokens":  u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		payload["input_tokens_details"] = u.InputTokensDetails
	}
	if u.OutputTokensDetails != nil {
		payload["output_tokens_details"] = u.OutputTokensDetails
	}
	mergeUsagePayload(payload, u.RawUsage, imageUsageKnownFields)
	return json.Marshal(payload)
}
