package exchange

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

type decisionBody struct {
	Model     string             `json:"model"`
	Input     []decisionMessage  `json:"input"`
	Questions []decisionQuestion `json:"questions"`
}

type decisionMessage struct {
	Role    string         `json:"role"`
	Content []decisionPart `json:"content"`
}

type decisionPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type decisionQuestion struct {
	Type         pluginapi.QuestionType `json:"type"`
	Name         string                 `json:"name,omitempty"`
	Instructions string                 `json:"instructions"`
	Choices      []decisionChoice       `json:"choices,omitempty"`
	Levels       []decisionLevel        `json:"levels,omitempty"`
}

type decisionChoice struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type decisionLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// DecisionRequestBody encodes a plugin's decision request as an OpenAI
// Decisions request body. Every message becomes a user message of its text
// and image parts; a message left with neither is dropped, and an input
// left empty is an error, since there would be nothing to decide on.
func DecisionRequestBody(req pluginapi.DecisionRequest) ([]byte, error) {
	body := decisionBody{Model: req.Model}
	for _, m := range req.Input {
		var parts []decisionPart
		for _, part := range m.Parts {
			switch part.Kind {
			case pluginapi.PartText:
				if part.Text != "" {
					parts = append(parts, decisionPart{Type: "input_text", Text: part.Text})
				}
			case pluginapi.PartImage:
				if url := imagePartURL(part); url != "" {
					parts = append(parts, decisionPart{Type: "input_image", ImageURL: url})
				}
			}
		}
		if len(parts) > 0 {
			body.Input = append(body.Input, decisionMessage{Role: string(pluginapi.RoleUser), Content: parts})
		}
	}
	if len(body.Input) == 0 {
		return nil, fmt.Errorf("exchange: the decision input has no text or image")
	}
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("exchange: a decision request needs at least one question")
	}
	for _, q := range req.Questions {
		question := decisionQuestion{Type: q.Type, Name: q.Name, Instructions: q.Instructions}
		for _, c := range q.Choices {
			question.Choices = append(question.Choices, decisionChoice(c))
		}
		for _, l := range q.Levels {
			question.Levels = append(question.Levels, decisionLevel(l))
		}
		body.Questions = append(body.Questions, question)
	}
	return json.Marshal(body)
}

// imagePartURL is an image part's URL, or a data URL of its inline bytes,
// or "" when it has neither.
func imagePartURL(part pluginapi.Part) string {
	if url := strings.TrimSpace(part.URL); url != "" {
		return url
	}
	if len(part.Data) == 0 {
		return ""
	}
	return "data:" + part.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
}

type decisionReply struct {
	Model   string                `json:"model"`
	Answers []decisionReplyAnswer `json:"answers"`
}

type decisionReplyAnswer struct {
	Name          *string                    `json:"name"`
	Type          string                     `json:"type"`
	Probability   *float64                   `json:"probability"`
	Choice        json.RawMessage            `json:"choice"`
	Score         float64                    `json:"score"`
	Confidence    float64                    `json:"confidence"`
	Probabilities []decisionReplyProbability `json:"probabilities"`
}

type decisionReplyProbability struct {
	Value       json.RawMessage `json:"value"`
	Label       string          `json:"label"`
	Probability float64         `json:"probability"`
}

// FromDecisionBody decodes an OpenAI Decision. A choice value is reported
// as text, so a boolean choice reads "true" or "false", and a score level by
// its label. A predicate answer without a probability between 0 and 1 is an
// error, so it never reads as a probability of zero.
func FromDecisionBody(body []byte) (*pluginapi.DecisionResult, error) {
	var reply decisionReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("exchange: decode decision: %w", err)
	}
	out := &pluginapi.DecisionResult{Model: reply.Model, Answers: make([]pluginapi.Answer, len(reply.Answers))}
	for i, a := range reply.Answers {
		if a.Type == string(pluginapi.QuestionPredicate) && (a.Probability == nil || *a.Probability < 0 || *a.Probability > 1) {
			return nil, fmt.Errorf("exchange: decision answer %d is a predicate without a probability between 0 and 1", i)
		}
		answer := pluginapi.Answer{
			Type:        a.Type,
			Choice:      choiceText(a.Choice),
			Score:       a.Score,
			Confidence:  a.Confidence,
		}
		if a.Name != nil {
			answer.Name = *a.Name
		}
		if a.Probability != nil {
			answer.Probability = *a.Probability
		}
		for _, p := range a.Probabilities {
			value := p.Label
			if a.Type != string(pluginapi.QuestionScore) {
				value = choiceText(p.Value)
			}
			answer.Probabilities = append(answer.Probabilities, pluginapi.AnswerProbability{Value: value, Probability: p.Probability})
		}
		out.Answers[i] = answer
	}
	return out, nil
}

// choiceText reads a choice value, a JSON string or boolean, as text.
func choiceText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}
