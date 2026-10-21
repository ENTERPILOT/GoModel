package jev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

var _ core.DecisionsProvider = (*Provider)(nil)

// maxSystemOneAnswerBytes caps the System One answer read for translation. A
// request holds at most a few hundred questions, so a real answer is far
// smaller; the cap keeps a misbehaving upstream from being buffered whole.
const maxSystemOneAnswerBytes = 8 << 20

// Decisions answers an OpenAI Decisions request with System One, which asks
// the same three kinds of question under other names: a predicate is a noul,
// and choice and score keep their names. The request is translated to a
// System One request, sent to /v1/systemone, and the answer translated back,
// so the caller sees OpenAI's shapes. Members System One has no place for
// (prompt caching, safety_identifier) are dropped. Images become System One
// image assets, which TypeSafe serves only with the
// TypeSafe-Danger-Zone-Images header; the gateway does not fetch images, so
// they must arrive inline as base64 data URLs.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil || req.Body == nil {
		return nil, core.NewInvalidRequestError("decisions request is required", nil)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	var decision decisionRequest
	if err := json.Unmarshal(raw, &decision); err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	plan, err := planSystemOne(&decision)
	if err != nil {
		return nil, err
	}
	// The gateway refuses images for models it knows to be text only, but a
	// model configured without listing (models_mode: allowlist) carries no
	// capability, so the provider checks too: a Kev server would read the
	// image references as text.
	if len(plan.request.Assets) > 0 && !p.hosted.Load() {
		return nil, core.NewInvalidRequestError("this jev provider points at a Kev server, which reads text only; send images to TypeSafe's hosted API (api.typesafe.ai) or another model that reads images", nil).WithParam("input")
	}
	body, err := json.Marshal(plan.request)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode System One request", err)
	}

	resp, err := p.Passthrough(ctx, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "systemone",
		Operation:    req.Operation,
		Model:        req.Model,
		Body:         io.NopCloser(bytes.NewReader(body)),
		Headers:      systemOneHeaders(req.Headers, len(plan.request.Assets) > 0),
		ProviderName: req.ProviderName,
	})
	if err != nil || resp == nil || resp.StatusCode >= http.StatusBadRequest {
		// Errors are relayed as the upstream wrote them; the gateway parses
		// them like any provider error.
		return resp, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemOneAnswerBytes+1))
	if err != nil {
		return nil, core.NewProviderError("jev", http.StatusBadGateway, "failed to read System One answer", err)
	}
	if len(answer) > maxSystemOneAnswerBytes {
		return nil, core.NewProviderError("jev", http.StatusBadGateway, "System One answer is too large to translate", nil)
	}
	translated, err := plan.decision(answer)
	if err != nil {
		return nil, core.NewProviderError("jev", http.StatusBadGateway, "failed to translate System One answer: "+err.Error(), err)
	}

	// The body is rewritten, so the upstream's length and encoding no longer
	// describe it.
	headers := http.Header(providers.CloneHTTPHeaders(resp.Headers))
	headers.Del("Content-Length")
	headers.Del("Content-Encoding")
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       io.NopCloser(bytes.NewReader(translated)),
	}, nil
}

// decisionRequest is the part of an OpenAI Decisions request System One can
// carry.
type decisionRequest struct {
	Model     string             `json:"model"`
	Input     json.RawMessage    `json:"input"`
	Questions []decisionQuestion `json:"questions"`
}

type decisionQuestion struct {
	Type         string           `json:"type"`
	Name         *string          `json:"name"`
	Instructions json.RawMessage  `json:"instructions"`
	Choices      []decisionChoice `json:"choices"`
	Levels       []decisionLevel  `json:"levels"`
}

type decisionChoice struct {
	// Value is a string or a boolean, kept as sent so the answer echoes the
	// caller's type.
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
}

type decisionLevel struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type decisionInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type decisionInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

type systemOneRequest struct {
	Model string `json:"model"`
	// State is the input's text, or, when the input holds images, its text
	// and image references in order.
	State     any           `json:"state"`
	Assets    orderedObject `json:"assets,omitempty"`
	Questions orderedObject `json:"questions"`
}

// orderedObject is a JSON object that keeps its members in the order they
// were added. A Go map would sort them, and order is meaningful to System
// One: models read choice options in the order sent, so reordering them
// changes the answer, and a fallback option the caller put last would move.
type orderedObject []orderedMember

type orderedMember struct {
	key   string
	value any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBufferString("{")
	for i, member := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(member.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(member.value)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

type systemOneQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions,omitempty"`
	Criteria     any             `json:"criteria,omitempty"`
}

// systemOnePlan is a translated request and what translating its answer back
// needs: the System One key and original question of each OpenAI question, in
// order.
type systemOnePlan struct {
	request   systemOneRequest
	keys      []string
	questions []decisionQuestion
	// choiceKeys maps, per choice question, a System One option name to the
	// value the caller sent.
	choiceKeys []map[string]json.RawMessage
}

func planSystemOne(req *decisionRequest) (*systemOnePlan, error) {
	state, assets, err := systemOneState(req.Input)
	if err != nil {
		return nil, err
	}
	if len(req.Questions) == 0 {
		return nil, core.NewInvalidRequestError("questions must hold at least one question", nil).WithParam("questions")
	}
	plan := &systemOnePlan{
		request:    systemOneRequest{Model: req.Model, State: state, Assets: assets, Questions: make(orderedObject, 0, len(req.Questions))},
		keys:       make([]string, len(req.Questions)),
		questions:  req.Questions,
		choiceKeys: make([]map[string]json.RawMessage, len(req.Questions)),
	}
	named := make(map[string]struct{}, len(req.Questions))
	for i, q := range req.Questions {
		if q.Name == nil {
			continue
		}
		if _, dup := named[*q.Name]; dup {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("questions[%d].name %q is not unique", i, *q.Name), nil).WithParam("questions")
		}
		named[*q.Name] = struct{}{}
	}
	for i, q := range req.Questions {
		key := unnamedQuestionKey(i, named)
		if q.Name != nil {
			key = *q.Name
		}
		question, choiceKeys, err := systemOneQuestionFor(i, q)
		if err != nil {
			return nil, err
		}
		plan.keys[i] = key
		plan.choiceKeys[i] = choiceKeys
		plan.request.Questions = append(plan.request.Questions, orderedMember{key: key, value: question})
	}
	return plan, nil
}

// unnamedQuestionKey names an unnamed question for System One, which keys
// questions by name, without taking a name the caller used.
func unnamedQuestionKey(index int, named map[string]struct{}) string {
	key := fmt.Sprintf("question_%d", index+1)
	for {
		if _, taken := named[key]; !taken {
			named[key] = struct{}{}
			return key
		}
		key += "_"
	}
}

func systemOneQuestionFor(index int, q decisionQuestion) (systemOneQuestion, map[string]json.RawMessage, error) {
	param := fmt.Sprintf("questions[%d]", index)
	switch q.Type {
	case "predicate":
		return systemOneQuestion{Type: "noul", Instructions: q.Instructions}, nil, nil
	case "choice":
		criteria := make(orderedObject, 0, len(q.Choices))
		keys := make(map[string]json.RawMessage, len(q.Choices))
		for _, choice := range q.Choices {
			key, err := choiceKey(choice.Value)
			if err != nil {
				return systemOneQuestion{}, nil, core.NewInvalidRequestError(param+".choices: "+err.Error(), err).WithParam("questions")
			}
			if _, dup := keys[key]; dup {
				return systemOneQuestion{}, nil, core.NewInvalidRequestError(fmt.Sprintf("%s.choices: value %s is not unique; models served by jev name options by their text, so a string and a boolean with the same text collide", param, key), nil).WithParam("questions")
			}
			keys[key] = choice.Value
			// A null description is System One's spelling for an option
			// whose name says it all.
			var description any
			if choice.Description != "" {
				description = choice.Description
			}
			criteria = append(criteria, orderedMember{key: key, value: description})
		}
		return systemOneQuestion{Type: "choice", Instructions: q.Instructions, Criteria: criteria}, keys, nil
	case "score":
		levels := make([]any, len(q.Levels))
		for i, level := range q.Levels {
			levels[i] = level.Label
			if level.Description != "" {
				levels[i] = map[string]string{"label": level.Label, "description": level.Description}
			}
		}
		return systemOneQuestion{Type: "score", Instructions: q.Instructions, Criteria: levels}, nil, nil
	default:
		return systemOneQuestion{}, nil, core.NewInvalidRequestError(fmt.Sprintf("%s.type %q is not a decision question type; use predicate, choice, or score", param, q.Type), nil).WithParam("questions")
	}
}

// choiceKey is the System One option name for a choice value: the string
// itself, or "true"/"false" for a boolean.
func choiceKey(value json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text, nil
	}
	var flag bool
	if err := json.Unmarshal(value, &flag); err == nil {
		return strconv.FormatBool(flag), nil
	}
	return "", fmt.Errorf("value %s must be a string or a boolean", strings.TrimSpace(string(value)))
}

// systemOneState builds the state System One evaluates from the input: a
// string input as is, and messages' text parts joined by blank lines. When
// the input holds images, the state is instead a list of the text parts and
// image references in input order, with each image listed once in assets
// under its reference; System One puts the image wherever its reference
// stands.
func systemOneState(input json.RawMessage) (any, orderedObject, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || core.IsJSONNull(trimmed) {
		return nil, nil, core.NewInvalidRequestError("input is required", nil).WithParam("input")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, nil, core.NewInvalidRequestError("invalid input: "+err.Error(), err).WithParam("input")
		}
		return text, nil, nil
	}
	var messages []decisionInputMessage
	if err := json.Unmarshal(trimmed, &messages); err != nil {
		return nil, nil, core.NewInvalidRequestError("input must be a string or a list of user messages", err).WithParam("input")
	}
	var (
		items  []any
		texts  []string
		assets orderedObject
	)
	for i, msg := range messages {
		content := bytes.TrimSpace(msg.Content)
		if len(content) > 0 && content[0] == '"' {
			var text string
			if err := json.Unmarshal(content, &text); err != nil {
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("invalid input[%d].content: %v", i, err), err).WithParam("input")
			}
			items, texts = append(items, text), append(texts, text)
			continue
		}
		var parts []decisionInputPart
		if err := json.Unmarshal(content, &parts); err != nil {
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("input[%d].content must be a string or a list of parts", i), err).WithParam("input")
		}
		for j, part := range parts {
			switch part.Type {
			case "input_text":
				items, texts = append(items, part.Text), append(texts, part.Text)
			case "input_image":
				asset, err := imageAsset(part.ImageURL)
				if err != nil {
					return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("input[%d].content[%d]: %v", i, j, err), err).WithParam("input")
				}
				ref := "image-" + uuid.NewString()
				items = append(items, ref)
				assets = append(assets, orderedMember{key: ref, value: asset})
			default:
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("input[%d] holds an %s part; models served by jev read text and images only", i, part.Type), nil).WithParam("input")
			}
		}
	}
	if len(assets) == 0 {
		return strings.Join(texts, "\n\n"), nil, nil
	}
	return items, assets, nil
}

// systemOneImage is a System One image asset sent inline.
type systemOneImage struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	DataBase64 string `json:"data_base64"`
}

// imageAsset turns an input_image URL into a System One image asset. System
// One takes PNG or JPEG bytes, and the gateway does not fetch remote images,
// so only a base64 data URL of one of those types is accepted.
func imageAsset(imageURL string) (systemOneImage, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(imageURL), "data:")
	if !ok {
		return systemOneImage{}, errors.New("models served by jev take images inline: send a base64 data URL (data:image/png;base64,...), not a link")
	}
	meta, data, ok := strings.Cut(rest, ",")
	mediaType, encoding, _ := strings.Cut(meta, ";")
	if !ok || !strings.EqualFold(encoding, "base64") || data == "" {
		return systemOneImage{}, errors.New("image_url must be a base64 data URL (data:image/png;base64,...)")
	}
	var format string
	switch strings.ToLower(mediaType) {
	case "image/png":
		format = "png"
	case "image/jpeg", "image/jpg":
		format = "jpg"
	default:
		return systemOneImage{}, fmt.Errorf("models served by jev read PNG and JPEG images, not %s", mediaType)
	}
	return systemOneImage{Type: "image", Format: format, DataBase64: data}, nil
}

// systemOneHeaders returns the headers to send upstream: the caller's, plus
// the opt-in TypeSafe requires for a request carrying images.
func systemOneHeaders(headers http.Header, images bool) http.Header {
	if !images {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = http.Header{}
	}
	out.Set("TypeSafe-Danger-Zone-Images", "true")
	return out
}

type systemOneAnswer struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type systemOneQuestionAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type decision struct {
	Answers []any         `json:"answers"`
	Model   string        `json:"model"`
	Usage   decisionUsage `json:"usage"`
}

type decisionUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CacheWriteTokens int `json:"cache_write_tokens"`
		CachedTokens     int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

type predicateAnswer struct {
	Name        *string `json:"name"`
	Type        string  `json:"type"`
	Probability float64 `json:"probability"`
}

type choiceAnswer struct {
	Name          *string             `json:"name"`
	Type          string              `json:"type"`
	Choice        json.RawMessage     `json:"choice"`
	Confidence    float64             `json:"confidence"`
	Probabilities []choiceProbability `json:"probabilities"`
}

type choiceProbability struct {
	Value       json.RawMessage `json:"value"`
	Probability float64         `json:"probability"`
}

type scoreAnswer struct {
	Name          *string            `json:"name"`
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities []scoreProbability `json:"probabilities"`
}

type scoreProbability struct {
	Label       string  `json:"label"`
	Probability float64 `json:"probability"`
	Value       int     `json:"value"`
}

type refusalAnswer struct {
	Name *string `json:"name"`
	Type string  `json:"type"`
}

// decision translates a System One answer into an OpenAI Decision: answers
// in question order, the name each question was sent with (null when it had
// none), and usage in the Responses shape. A question System One left
// unanswered, or answered with another type, becomes a refusal, which the
// Decisions API allows for any question.
func (p *systemOnePlan) decision(body []byte) ([]byte, error) {
	var answer systemOneAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, err
	}
	out := decision{Answers: make([]any, len(p.questions)), Model: answer.Model}
	for i, q := range p.questions {
		out.Answers[i] = p.answerFor(i, q, answer.Answers[p.keys[i]])
	}
	out.Usage.InputTokens = answer.Usage.InputTokens
	out.Usage.OutputTokens = answer.Usage.OutputTokens
	out.Usage.TotalTokens = answer.Usage.InputTokens + answer.Usage.OutputTokens
	return json.Marshal(out)
}

func (p *systemOnePlan) answerFor(index int, q decisionQuestion, raw json.RawMessage) any {
	refusal := refusalAnswer{Name: q.Name, Type: "refusal"}
	var a systemOneQuestionAnswer
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return refusal
	}
	switch {
	case q.Type == "predicate" && a.Type == "noul" && a.Noul != nil:
		return predicateAnswer{Name: q.Name, Type: "predicate", Probability: *a.Noul}
	case q.Type == "choice" && a.Type == "choice":
		chosen, ok := p.choiceKeys[index][a.Choice]
		if !ok {
			return refusal
		}
		probabilities := make([]choiceProbability, len(q.Choices))
		for i, choice := range q.Choices {
			key, _ := choiceKey(choice.Value)
			probabilities[i] = choiceProbability{Value: choice.Value, Probability: a.Probabilities[key]}
		}
		return choiceAnswer{Name: q.Name, Type: "choice", Choice: chosen, Confidence: a.Confidence, Probabilities: probabilities}
	case q.Type == "score" && a.Type == "score" && a.Score != nil:
		probabilities := make([]scoreProbability, len(q.Levels))
		for i, level := range q.Levels {
			probabilities[i] = scoreProbability{Label: level.Label, Probability: a.Probabilities[strconv.Itoa(i)], Value: i}
		}
		return scoreAnswer{Name: q.Name, Type: "score", Score: *a.Score, Confidence: a.Confidence, Probabilities: probabilities}
	default:
		return refusal
	}
}
