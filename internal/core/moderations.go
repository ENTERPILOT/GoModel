package core

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"
)

// DefaultModerationModel is the model a POST /v1/moderations request uses when
// it names none, matching OpenAI's own default.
const DefaultModerationModel = "omni-moderation-latest"

// ModerationRequest is an OpenAI-compatible POST /v1/moderations request.
// Input stays raw so the OpenAI provider forwards it verbatim; it is a string,
// an array of strings, or an array of text / image_url parts (multimodal
// models). Unknown fields are kept in ExtraFields and forwarded upstream.
type ModerationRequest struct {
	Model string `json:"model,omitempty"`
	// Input is a string, an array of strings, or an array of text and
	// image_url parts.
	Input json.RawMessage `json:"input" binding:"required" swaggertype:"object"`

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string `json:"provider,omitempty"`

	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

func (r *ModerationRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model    string          `json:"model"`
		Input    json.RawMessage `json:"input"`
		Provider string          `json:"provider,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data, "model", "input", "provider")
	if err != nil {
		return err
	}
	r.Model = raw.Model
	r.Input = raw.Input
	r.Provider = raw.Provider
	r.ExtraFields = extraFields
	return nil
}

func (r ModerationRequest) MarshalJSON() ([]byte, error) {
	// alias inherits the fields and tags but drops MarshalJSON so json.Marshal
	// does not recurse; ExtraFields (json:"-") is merged separately.
	type alias ModerationRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

// ModerationInput is one classifiable input: the text to check and, for a
// multimodal input, the image URLs that go with it.
type ModerationInput struct {
	Text      string
	ImageURLs []string
}

// moderationPart is one element of a multimodal input array.
type moderationPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// ModerationInputs parses Input into the inputs it classifies, one result
// each: a string is one input, an array of strings is one input per string,
// and an array of text / image_url parts is a single multimodal input.
func (r *ModerationRequest) ModerationInputs() ([]ModerationInput, error) {
	raw := bytes.TrimSpace(r.Input)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, NewInvalidRequestError("input is required", nil)
	}
	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, NewInvalidRequestError("input must be a string or an array", err)
		}
		return []ModerationInput{{Text: text}}, nil
	case '[':
	default:
		return nil, NewInvalidRequestError("input must be a string or an array", nil)
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, NewInvalidRequestError("input must be a string or an array", err)
	}
	if len(items) == 0 {
		return nil, NewInvalidRequestError("input must not be empty", nil)
	}
	if first := bytes.TrimSpace(items[0]); len(first) > 0 && first[0] == '{' {
		input, err := parseModerationParts(items)
		if err != nil {
			return nil, err
		}
		return []ModerationInput{input}, nil
	}
	inputs := make([]ModerationInput, 0, len(items))
	for _, item := range items {
		var text string
		if item = bytes.TrimSpace(item); len(item) == 0 || item[0] != '"' {
			return nil, NewInvalidRequestError("input array must hold only strings or only text and image_url objects", nil)
		}
		if err := json.Unmarshal(item, &text); err != nil {
			return nil, NewInvalidRequestError("input array must hold only strings or only text and image_url objects", err)
		}
		inputs = append(inputs, ModerationInput{Text: text})
	}
	return inputs, nil
}

func parseModerationParts(items []json.RawMessage) (ModerationInput, error) {
	var input ModerationInput
	var texts []string
	for _, item := range items {
		var part moderationPart
		if err := json.Unmarshal(item, &part); err != nil {
			return ModerationInput{}, NewInvalidRequestError("input array must hold only strings or only text and image_url objects", err)
		}
		switch part.Type {
		case "text":
			if part.Text == nil {
				return ModerationInput{}, NewInvalidRequestError("text input requires a text string", nil)
			}
			texts = append(texts, *part.Text)
		case "image_url":
			if part.ImageURL == nil || strings.TrimSpace(part.ImageURL.URL) == "" {
				return ModerationInput{}, NewInvalidRequestError("image_url input requires image_url.url", nil)
			}
			input.ImageURLs = append(input.ImageURLs, part.ImageURL.URL)
		default:
			return ModerationInput{}, NewInvalidRequestError("input object type must be text or image_url, got "+quoteOrEmpty(part.Type), nil)
		}
	}
	input.Text = strings.Join(texts, "\n")
	return input, nil
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return "an empty type"
	}
	return `"` + s + `"`
}

// DecodeModerationRequest decodes a JSON moderation request body.
func DecodeModerationRequest(body []byte) (*ModerationRequest, error) {
	var req ModerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, NewInvalidRequestError("invalid moderation request: "+err.Error(), err)
	}
	return &req, nil
}

// ValidateModerationRequest fills the default model and checks the input
// shape before the request is routed.
func ValidateModerationRequest(req *ModerationRequest) error {
	if req == nil {
		return NewInvalidRequestError("moderation request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = DefaultModerationModel
	}
	_, err := req.ModerationInputs()
	return err
}

// ModerationResponse is the OpenAI-compatible moderation response.
type ModerationResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Results []ModerationResult `json:"results"`

	// Provider is a gateway addition, stamped like every other routed response,
	// so clients can tell which provider served the request.
	Provider string `json:"provider,omitempty"`
}

// ModerationResult is the verdict for one input. Category names are the
// provider's own: OpenAI's (harassment, violence, ...) or, for Mistral, its
// taxonomy (violence_and_threats, pii, ...).
type ModerationResult struct {
	Flagged                   bool                `json:"flagged"`
	Categories                map[string]bool     `json:"categories"`
	CategoryScores            map[string]float64  `json:"category_scores"`
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types,omitempty"`
}
