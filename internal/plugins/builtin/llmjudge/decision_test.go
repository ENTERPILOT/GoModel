package llmjudge

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
)

// decisionHost answers every decision question with probability and lists
// jev/jev-latest as a decision model.
func decisionHost(probability float64) *plugintest.Host {
	return &plugintest.Host{
		DecisionModels: []string{"jev/jev-latest"},
		Answer: func(req pluginapi.DecisionRequest) (*pluginapi.DecisionResult, error) {
			return &pluginapi.DecisionResult{Model: "jev-1.13.0", Answers: []pluginapi.Answer{
				{Name: req.Questions[0].Name, Type: "predicate", Probability: probability},
			}}, nil
		},
	}
}

// With the default engine a decision model is asked the decision question
// about the judged text, without tags, and blocks at or above the threshold.
func TestDecisionEngineBlocksOnProbability(t *testing.T) {
	tests := []struct {
		name        string
		cfg         string
		probability float64
		action      pluginapi.Action
		code        string
	}{
		{"above threshold blocks", `{"model": "jev/jev-latest"}`, 0.91, pluginapi.ActionBlock, Code},
		{"at threshold blocks", `{"model": "jev/jev-latest"}`, 0.5, pluginapi.ActionBlock, Code},
		{"below threshold allows", `{"model": "jev/jev-latest"}`, 0.2, pluginapi.ActionAllow, ""},
		{"custom threshold", `{"model": "jev/jev-latest", "block_threshold": 0.95}`, 0.91, pluginapi.ActionAllow, ""},
		{"warn action", `{"model": "jev/jev-latest", "action": "warn"}`, 0.91, pluginapi.ActionWarn, Code},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := decisionHost(tt.probability)
			p := newPlugin(t, tt.cfg, host)
			d, err := p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
			require.NoError(t, err)
			assert.Equal(t, tt.action, d.Action)
			assert.Equal(t, tt.code, d.Code)
			detail := d.Detail.(map[string]any)
			assert.InDelta(t, tt.probability, detail["probability"], 1e-9)
			assert.Equal(t, "jev/jev-latest", detail["judge_model"])

			assert.Empty(t, host.Requests(), "no chat completion is sent")
			require.Len(t, host.DecisionRequests(), 1)
			req := host.DecisionRequests()[0]
			assert.Equal(t, "jev/jev-latest", req.Model)
			require.Len(t, req.Input, 1)
			assert.Equal(t, pluginapi.RoleUser, req.Input[0].Role)
			assert.Equal(t, "second question", req.Input[0].Text())
			assert.Equal(t, []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Name: questionName, Instructions: DefaultDecisionQuestion}}, req.Questions)
		})
	}
}

// Auto asks a decision model by decision and any other model by chat; the
// engine setting overrides that either way.
func TestEngineSelection(t *testing.T) {
	tests := []struct {
		name     string
		cfg      string
		decision bool
	}{
		{"auto decision model", `{"model": "jev/jev-latest"}`, true},
		{"auto chat model", `{"model": "openai/gpt-4o-mini"}`, false},
		{"chat engine on a decision model", `{"model": "jev/jev-latest", "engine": "chat"}`, false},
		{"decision engine on another model", `{"model": "openai/gpt-6-luna", "engine": "decision", "decision_question": "Is it spam?"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := decisionHost(0.1)
			host.Replies = []string{`{"verdict":"allow","reason":"fine"}`}
			p := newPlugin(t, tt.cfg, host)
			d, err := p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
			require.NoError(t, err)
			assert.Equal(t, pluginapi.ActionAllow, d.Action)
			if tt.decision {
				assert.Len(t, host.DecisionRequests(), 1)
				assert.Empty(t, host.Requests())
				return
			}
			assert.Empty(t, host.DecisionRequests())
			assert.Len(t, host.Requests(), 1)
		})
	}

	// A host without decisions asks every model by chat under auto.
	host := plugintest.NewHost(`{"verdict":"allow"}`)
	p := newPlugin(t, `{"model": "jev/jev-latest"}`, host)
	p.host = chatOnlyHost{host}
	_, err := p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
	require.NoError(t, err)
	assert.Len(t, host.Requests(), 1)
}

// A question the decision model refuses is unclear; a decision without an
// answer is no verdict; both follow on_unclear. A failed call is an error,
// which the instance's fail mode handles.
func TestDecisionEngineUnclearAnswers(t *testing.T) {
	answer := func(result *pluginapi.DecisionResult, err error) *plugintest.Host {
		return &plugintest.Host{DecisionModels: []string{"jev/jev-latest"}, Answer: func(pluginapi.DecisionRequest) (*pluginapi.DecisionResult, error) {
			return result, err
		}}
	}

	p := newPlugin(t, `{"model": "jev/jev-latest"}`, answer(&pluginapi.DecisionResult{Answers: []pluginapi.Answer{{Type: pluginapi.AnswerRefusal}}}, nil))
	d, err := p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionWarn, d.Action)
	assert.Equal(t, CodeUnclear, d.Code)
	assert.Equal(t, "decision model declined to answer", d.Detail.(map[string]any)["reason"])

	p = newPlugin(t, `{"model": "jev/jev-latest", "on_unclear": "block"}`, answer(&pluginapi.DecisionResult{}, nil))
	d, err = p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	assert.Equal(t, CodeNoVerdict, d.Code)

	p = newPlugin(t, `{"model": "jev/jev-latest"}`, answer(&pluginapi.DecisionResult{Answers: []pluginapi.Answer{{Type: "choice", Choice: "yes"}}}, nil))
	d, err = p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
	require.NoError(t, err)
	assert.Equal(t, CodeUnclear, d.Code)

	upstream := errors.New("kev is down")
	p = newPlugin(t, `{"model": "jev/jev-latest"}`, answer(nil, upstream))
	_, err = p.OnPrompt(context.Background(), plugintest.Exchange(prompt(), nil))
	require.ErrorIs(t, err, upstream)
	assert.ErrorContains(t, err, "judge call failed")
}

func imagePrompt(text string) *pluginapi.Prompt {
	user := pluginapi.Message{ID: "m0", Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartImage, URL: "data:image/png;base64,iVBORw0KGgo="},
	}}
	if text != "" {
		user.Parts = append([]pluginapi.Part{{Kind: pluginapi.PartText, Text: text}}, user.Parts...)
	}
	return plugintest.Prompt(user)
}

// With judge_images a decision model also sees the judged messages' images,
// so a prompt that is only an image is judged; without it, or with a chat
// judge, images are not sent and an image-only prompt is allowed unjudged.
func TestDecisionEngineJudgesImagesWhenAsked(t *testing.T) {
	host := decisionHost(0.9)
	p := newPlugin(t, `{"model": "jev/jev-latest", "judge_images": true}`, host)
	d, err := p.OnPrompt(context.Background(), plugintest.Exchange(imagePrompt("what is this?"), nil))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	parts := host.DecisionRequests()[0].Input[0].Parts
	require.Len(t, parts, 2)
	assert.Equal(t, pluginapi.Part{Kind: pluginapi.PartText, Text: "what is this?"}, parts[0])
	assert.Equal(t, pluginapi.Part{Kind: pluginapi.PartImage, URL: "data:image/png;base64,iVBORw0KGgo="}, parts[1])

	d, err = p.OnPrompt(context.Background(), plugintest.Exchange(imagePrompt(""), nil))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	assert.Len(t, host.DecisionRequests(), 2)

	// An image given as inline bytes is sent as a data URL.
	inline := plugintest.Prompt(pluginapi.Message{ID: "m0", Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartImage, MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
	}})
	_, err = p.OnPrompt(context.Background(), plugintest.Exchange(inline, nil))
	require.NoError(t, err)
	require.Len(t, host.DecisionRequests(), 3)
	assert.Equal(t, "data:image/png;base64,iVBORw==", host.DecisionRequests()[2].Input[0].Parts[0].URL)

	for _, cfg := range []string{`{"model": "jev/jev-latest"}`, `{"model": "jev/jev-latest", "engine": "chat", "judge_images": true}`} {
		host := decisionHost(0.9)
		p := newPlugin(t, cfg, host)
		d, err := p.OnPrompt(context.Background(), plugintest.Exchange(imagePrompt(""), nil))
		require.NoError(t, err)
		assert.Equal(t, pluginapi.ActionAllow, d.Action, cfg)
		assert.Empty(t, host.DecisionRequests(), cfg)
		assert.Empty(t, host.Requests(), cfg)
	}
}

// The response phase asks about the completion's text, and a verdict is
// reused within the exchange like a chat verdict.
func TestDecisionEngineResponsesAndCache(t *testing.T) {
	host := decisionHost(0.8)
	p := newPlugin(t, `{"model": "jev/jev-latest", "action": "warn"}`, host)
	x := plugintest.Exchange(&pluginapi.Prompt{Messages: []pluginapi.Message{plugintest.Text(pluginapi.RoleUser, "m0", "same text")}},
		plugintest.Completion("same text"))

	first, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	second, err := p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionWarn, first.Action)
	assert.Equal(t, true, second.Detail.(map[string]any)["cached"])
	require.Len(t, host.DecisionRequests(), 1)
	assert.Equal(t, []float64{0.8}, host.Recorded().Values["decision_probability"])

	x.Response.Choices[0].Message.Parts[0].Text = "other text"
	_, err = p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	require.Len(t, host.DecisionRequests(), 2)
	assert.Equal(t, "other text", host.DecisionRequests()[1].Input[0].Text())
}

func TestSummarizeEngine(t *testing.T) {
	p := New()
	assert.Equal(t, "jev/jev-latest, block, target auto, unclear: warn, engine decision", p.(*Plugin).Summarize([]byte(`{"model": "jev/jev-latest", "engine": "decision"}`)))
}
