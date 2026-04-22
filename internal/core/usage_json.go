package core

import (
	"encoding/json"
)

var usageKnownFields = map[string]struct{}{
	"prompt_tokens":             {},
	"completion_tokens":         {},
	"total_tokens":              {},
	"prompt_tokens_details":     {},
	"completion_tokens_details": {},
	"raw_usage":                 {},
}

var responsesUsageKnownFields = map[string]struct{}{
	"input_tokens":              {},
	"output_tokens":             {},
	"total_tokens":              {},
	"input_tokens_details":      {},
	"output_tokens_details":     {},
	"prompt_tokens_details":     {},
	"completion_tokens_details": {},
	"raw_usage":                 {},
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	var rawUsage map[string]any
	for key, raw := range payload {
		switch key {
		case "prompt_tokens":
			if err := json.Unmarshal(raw, &u.PromptTokens); err != nil {
				return err
			}
		case "completion_tokens":
			if err := json.Unmarshal(raw, &u.CompletionTokens); err != nil {
				return err
			}
		case "total_tokens":
			if err := json.Unmarshal(raw, &u.TotalTokens); err != nil {
				return err
			}
		case "prompt_tokens_details":
			if err := json.Unmarshal(raw, &u.PromptTokensDetails); err != nil {
				return err
			}
		case "completion_tokens_details":
			if err := json.Unmarshal(raw, &u.CompletionTokensDetails); err != nil {
				return err
			}
		case "raw_usage":
			if err := mergeRawUsageObject(&rawUsage, raw); err != nil {
				return err
			}
		default:
			if _, known := usageKnownFields[key]; known {
				continue
			}
			if err := mergeRawUsageField(&rawUsage, key, raw); err != nil {
				return err
			}
		}
	}

	u.RawUsage = rawUsage
	return nil
}

func (u Usage) MarshalJSON() ([]byte, error) {
	return marshalTokenUsageJSON(
		u.RawUsage,
		usageJSONPayload{
			PromptTokens:            u.PromptTokens,
			CompletionTokens:        u.CompletionTokens,
			TotalTokens:             u.TotalTokens,
			PromptTokensDetails:     u.PromptTokensDetails,
			CompletionTokensDetails: u.CompletionTokensDetails,
		},
		func(payload map[string]any) {
			payload["prompt_tokens"] = u.PromptTokens
			payload["completion_tokens"] = u.CompletionTokens
			payload["total_tokens"] = u.TotalTokens
		},
		u.PromptTokensDetails,
		"prompt_tokens_details",
		u.CompletionTokensDetails,
		"completion_tokens_details",
		usageKnownFields,
	)
}

func (u *ResponsesUsage) UnmarshalJSON(data []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	var rawUsage map[string]any
	for key, raw := range payload {
		switch key {
		case "input_tokens":
			if err := json.Unmarshal(raw, &u.InputTokens); err != nil {
				return err
			}
		case "output_tokens":
			if err := json.Unmarshal(raw, &u.OutputTokens); err != nil {
				return err
			}
		case "total_tokens":
			if err := json.Unmarshal(raw, &u.TotalTokens); err != nil {
				return err
			}
		case "input_tokens_details", "prompt_tokens_details":
			if err := json.Unmarshal(raw, &u.PromptTokensDetails); err != nil {
				return err
			}
		case "output_tokens_details", "completion_tokens_details":
			if err := json.Unmarshal(raw, &u.CompletionTokensDetails); err != nil {
				return err
			}
		case "raw_usage":
			if err := mergeRawUsageObject(&rawUsage, raw); err != nil {
				return err
			}
		default:
			if _, known := responsesUsageKnownFields[key]; known {
				continue
			}
			if err := mergeRawUsageField(&rawUsage, key, raw); err != nil {
				return err
			}
		}
	}

	u.RawUsage = rawUsage
	return nil
}

func (u ResponsesUsage) MarshalJSON() ([]byte, error) {
	return marshalTokenUsageJSON(
		u.RawUsage,
		responsesUsageJSONPayload{
			InputTokens:             u.InputTokens,
			OutputTokens:            u.OutputTokens,
			TotalTokens:             u.TotalTokens,
			PromptTokensDetails:     u.PromptTokensDetails,
			CompletionTokensDetails: u.CompletionTokensDetails,
		},
		func(payload map[string]any) {
			payload["input_tokens"] = u.InputTokens
			payload["output_tokens"] = u.OutputTokens
			payload["total_tokens"] = u.TotalTokens
		},
		u.PromptTokensDetails,
		"input_tokens_details",
		u.CompletionTokensDetails,
		"output_tokens_details",
		responsesUsageKnownFields,
	)
}

type usageJSONPayload struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type responsesUsageJSONPayload struct {
	InputTokens             int                      `json:"input_tokens"`
	OutputTokens            int                      `json:"output_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"input_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"output_tokens_details,omitempty"`
}

func marshalUsagePayload(
	rawUsage map[string]any,
	populateCounts func(map[string]any),
	promptDetails *PromptTokensDetails,
	promptDetailsKey string,
	completionDetails *CompletionTokensDetails,
	completionDetailsKey string,
	knownFields map[string]struct{},
) ([]byte, error) {
	payload := make(map[string]any, 3+2+len(rawUsage))
	populateCounts(payload)
	if promptDetails != nil {
		payload[promptDetailsKey] = promptDetails
	}
	if completionDetails != nil {
		payload[completionDetailsKey] = completionDetails
	}
	mergeUsagePayload(payload, rawUsage, knownFields)
	return json.Marshal(payload)
}

func marshalTokenUsageJSON(
	rawUsage map[string]any,
	emptyPayload any,
	populateCounts func(map[string]any),
	promptDetails *PromptTokensDetails,
	promptDetailsKey string,
	completionDetails *CompletionTokensDetails,
	completionDetailsKey string,
	knownFields map[string]struct{},
) ([]byte, error) {
	if len(rawUsage) == 0 {
		return json.Marshal(emptyPayload)
	}
	return marshalUsagePayload(
		rawUsage,
		populateCounts,
		promptDetails,
		promptDetailsKey,
		completionDetails,
		completionDetailsKey,
		knownFields,
	)
}

func mergeRawUsageField(dst *map[string]any, key string, raw json.RawMessage) error {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if *dst == nil {
		*dst = make(map[string]any)
	}
	(*dst)[key] = decoded
	return nil
}

func mergeRawUsageObject(dst *map[string]any, raw json.RawMessage) error {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if len(decoded) == 0 {
		return nil
	}
	if *dst == nil {
		*dst = make(map[string]any, len(decoded))
	}
	for key, value := range decoded {
		(*dst)[key] = value
	}
	return nil
}

func mergeUsagePayload(payload map[string]any, rawUsage map[string]any, knownFields map[string]struct{}) {
	for key, value := range rawUsage {
		if _, known := knownFields[key]; known {
			continue
		}
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
	}
}
