package core

import (
	"maps"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
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

// usageMembers is the typed part of Usage's JSON form. It has no RawUsage and
// no methods, so decoding into it neither recurses nor captures raw_usage.
type usageMembers struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details"`
}

// UnmarshalJSON decodes the typed members in one pass and builds RawUsage
// from the other members only when there are any.
func (u *Usage) UnmarshalJSON(data []byte) error {
	rawUsage, irregular, scanErr := scanUsageMembers(data)
	if irregular {
		return u.unmarshalJSONMemberByMember(data)
	}
	members := usageMembers{
		PromptTokens:            u.PromptTokens,
		CompletionTokens:        u.CompletionTokens,
		TotalTokens:             u.TotalTokens,
		PromptTokensDetails:     u.PromptTokensDetails,
		CompletionTokensDetails: u.CompletionTokensDetails,
	}
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	u.PromptTokens = members.PromptTokens
	u.CompletionTokens = members.CompletionTokens
	u.TotalTokens = members.TotalTokens
	u.PromptTokensDetails = members.PromptTokensDetails
	u.CompletionTokensDetails = members.CompletionTokensDetails
	u.RawUsage = rawUsage
	return nil
}

// scanUsageMembers collects the members usageMembers does not decode into a
// RawUsage map. It reports irregular for objects the typed decode would read
// differently from the member-by-member decode: a typed or raw_usage member
// given twice (the typed decode merges repeated detail objects, the other
// keeps the last), and keys that are escaped, non-ASCII or a case variant of
// a typed member (the typed decode matches keys case-insensitively).
func scanUsageMembers(data []byte) (rawUsage map[string]any, irregular bool, err error) {
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return nil, false, nil
	}
	var seen uint8
	root.ForEach(func(key, value gjson.Result) bool {
		if !isPlainJSONKey(key.Raw) {
			irregular = true
			return false
		}
		name := key.String()
		if bit := usageMemberBit(name); bit != 0 {
			if seen&bit != 0 {
				irregular = true
				return false
			}
			seen |= bit
			if name == "raw_usage" {
				err = mergeRawUsageObject(&rawUsage, rawJSONValue(data, value))
			}
			return err == nil
		}
		if foldsToUsageMember(name) {
			irregular = true
			return false
		}
		err = mergeRawUsageField(&rawUsage, name, rawJSONValue(data, value))
		return err == nil
	})
	return rawUsage, irregular, err
}

func usageMemberBit(name string) uint8 {
	switch name {
	case "prompt_tokens":
		return 1 << 0
	case "completion_tokens":
		return 1 << 1
	case "total_tokens":
		return 1 << 2
	case "prompt_tokens_details":
		return 1 << 3
	case "completion_tokens_details":
		return 1 << 4
	case "raw_usage":
		return 1 << 5
	}
	return 0
}

func foldsToUsageMember(name string) bool {
	for known := range usageKnownFields {
		if strings.EqualFold(name, known) {
			return true
		}
	}
	return false
}

// isPlainJSONKey reports whether a raw key is printable ASCII without
// escapes, so its bytes are its name.
func isPlainJSONKey(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c == '\\' || c < 0x20 || c >= 0x80 {
			return false
		}
	}
	return true
}

// rawJSONValue returns value's bytes as a slice of data, which gjson parsed,
// instead of copying the string gjson returns.
func rawJSONValue(data []byte, value gjson.Result) json.RawMessage {
	if end := value.Index + len(value.Raw); value.Index > 0 && end <= len(data) {
		return data[value.Index:end]
	}
	return json.RawMessage(value.Raw)
}

// unmarshalJSONMemberByMember decodes each member on its own. UnmarshalJSON
// falls back to it for the irregular objects scanUsageMembers reports.
func (u *Usage) unmarshalJSONMemberByMember(data []byte) error {
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

// MarshalJSON emits the OpenAI Responses usage shape and nothing else. Unlike
// Chat Completions, which passes provider usage extras through at the top
// level, the Responses API returns a closed object, so provider-specific
// members (thoughts_token_count, completion_reasoning_tokens,
// cache_creation_input_tokens, …) stay in RawUsage for usage records and cost
// calculation instead of leaking into the client response. Everything with an
// OpenAI-shaped home is kept: reasoning tokens under output_tokens_details,
// cached tokens under input_tokens_details.
func (u ResponsesUsage) MarshalJSON() ([]byte, error) {
	return json.Marshal(responsesUsageJSONPayload{
		InputTokens:             u.InputTokens,
		OutputTokens:            u.OutputTokens,
		TotalTokens:             u.TotalTokens,
		PromptTokensDetails:     u.PromptTokensDetails,
		CompletionTokensDetails: u.CompletionTokensDetails,
	})
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
	maps.Copy(*dst, decoded)
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
