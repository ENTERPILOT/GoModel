package exchange

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Every message is sent as a user message of its text and image parts, and
// the questions keep their order and options.
func TestDecisionRequestBody(t *testing.T) {
	body, err := DecisionRequestBody(pluginapi.DecisionRequest{
		Model: "jev/jev-latest",
		Input: []pluginapi.Message{
			pluginapi.TextMessage(pluginapi.RoleSystem, "Look at this."),
			{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
				{Kind: pluginapi.PartText, Text: "A receipt"},
				{Kind: pluginapi.PartImage, URL: "data:image/png;base64,iVBORw0KGgo="},
				{Kind: pluginapi.PartAudio, Data: []byte("UklGR")},
			}},
			{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{{Kind: pluginapi.PartAudio, Data: []byte("UklGR")}}},
		},
		Questions: []pluginapi.Question{
			{Type: pluginapi.QuestionPredicate, Name: "unsafe", Instructions: "Is it unsafe?"},
			{Type: pluginapi.QuestionChoice, Name: "topic", Instructions: "Topic?", Choices: []pluginapi.ChoiceOption{{Value: "food"}, {Value: "travel", Description: "trips"}}},
			{Type: pluginapi.QuestionScore, Instructions: "Severity?", Levels: []pluginapi.ScoreLevel{{Label: "low"}, {Label: "high", Description: "serious"}}},
		},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model":"jev/jev-latest",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Look at this."}]},
			{"role":"user","content":[{"type":"input_text","text":"A receipt"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}
		],
		"questions":[
			{"type":"predicate","name":"unsafe","instructions":"Is it unsafe?"},
			{"type":"choice","name":"topic","instructions":"Topic?","choices":[{"value":"food"},{"value":"travel","description":"trips"}]},
			{"type":"score","instructions":"Severity?","levels":[{"label":"low"},{"label":"high","description":"serious"}]}
		]}`, string(body))
}

func TestDecisionRequestBodyNeedsInputAndQuestions(t *testing.T) {
	question := []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Instructions: "?"}}

	_, err := DecisionRequestBody(pluginapi.DecisionRequest{Model: "m", Questions: question,
		Input: []pluginapi.Message{{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{{Kind: pluginapi.PartAudio}}}}})
	require.ErrorContains(t, err, "no text or image")

	_, err = DecisionRequestBody(pluginapi.DecisionRequest{Model: "m", Input: []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "x")}})
	require.ErrorContains(t, err, "at least one question")
}

// Answers decode in order, a boolean choice reads as text, a score
// distribution by level label, and a refusal keeps its type.
func TestFromDecisionBody(t *testing.T) {
	result, err := FromDecisionBody([]byte(`{"model":"jev-1.13.0","answers":[
		{"name":"unsafe","type":"predicate","probability":0.91},
		{"name":"ok","type":"choice","choice":true,"confidence":0.8,"probabilities":[{"value":true,"probability":0.8},{"value":false,"probability":0.2}]},
		{"name":null,"type":"score","score":1.4,"confidence":0.6,"probabilities":[{"label":"low","probability":0.4,"value":0},{"label":"high","probability":0.6,"value":1}]},
		{"name":"topic","type":"refusal"}
	],"usage":{"input_tokens":12}}`))
	require.NoError(t, err)
	assert.Equal(t, "jev-1.13.0", result.Model)
	require.Len(t, result.Answers, 4)

	assert.Equal(t, pluginapi.Answer{Name: "unsafe", Type: "predicate", Probability: 0.91}, result.Answers[0])
	assert.Equal(t, "true", result.Answers[1].Choice)
	assert.Equal(t, []pluginapi.AnswerProbability{{Value: "true", Probability: 0.8}, {Value: "false", Probability: 0.2}}, result.Answers[1].Probabilities)
	assert.Empty(t, result.Answers[2].Name)
	assert.InDelta(t, 1.4, result.Answers[2].Score, 1e-9)
	assert.Equal(t, []pluginapi.AnswerProbability{{Value: "low", Probability: 0.4}, {Value: "high", Probability: 0.6}}, result.Answers[2].Probabilities)
	assert.True(t, result.Answers[3].Refused())
	assert.False(t, result.Answers[0].Refused())

	_, err = FromDecisionBody([]byte(`not json`))
	require.Error(t, err)
}

// An image given as inline bytes is sent as a data URL, as the chat encoder
// sends it.
func TestDecisionRequestBodyEncodesInlineImages(t *testing.T) {
	body, err := DecisionRequestBody(pluginapi.DecisionRequest{
		Model: "jev/jev-latest",
		Input: []pluginapi.Message{{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartImage, MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		}}},
		Questions: []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Instructions: "?"}},
	})
	require.NoError(t, err)
	assert.Contains(t, string(body), `{"type":"input_image","image_url":"data:image/png;base64,iVBORw=="}`)
}

// A predicate answer without a probability, or with one outside 0 to 1, is
// an error rather than a probability of zero.
func TestFromDecisionBodyRejectsPredicatesWithoutProbability(t *testing.T) {
	for _, answer := range []string{
		`{"type":"predicate"}`,
		`{"type":"predicate","probability":null}`,
		`{"type":"predicate","probability":1.5}`,
		`{"type":"predicate","probability":-0.1}`,
	} {
		_, err := FromDecisionBody([]byte(`{"answers":[` + answer + `]}`))
		require.ErrorContains(t, err, "without a probability between 0 and 1", answer)
	}
	result, err := FromDecisionBody([]byte(`{"answers":[{"type":"predicate","probability":0},{"type":"refusal"}]}`))
	require.NoError(t, err)
	assert.Zero(t, result.Answers[0].Probability)
	assert.True(t, result.Answers[1].Refused())
}
