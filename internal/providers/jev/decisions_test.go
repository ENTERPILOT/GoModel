package jev

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

var _ core.DecisionsProvider = (*Provider)(nil)

// decisionsRequest asks one question of each type, the score unnamed, with a
// boolean choice value and prompt caching System One has no place for.
const decisionsRequest = `{
  "model": "jev-latest",
  "input": [{"type": "message", "role": "user", "content": [
    {"type": "input_text", "text": "Shoes arrived late."},
    {"type": "input_text", "text": "I was charged twice.", "prompt_cache_breakpoint": {"mode": "explicit"}}
  ]}],
  "questions": [
    {"type": "predicate", "name": "escalate", "instructions": "Does this need urgent attention?"},
    {"type": "choice", "name": "department", "instructions": "Which team?",
     "choices": [{"value": "billing", "description": "Charges"}, {"value": "shipping"}, {"value": false, "description": "Neither"}]},
    {"type": "score", "instructions": "How frustrated?", "levels": [{"label": "Calm"}, {"label": "Angry", "description": "Raised voice"}]}
  ],
  "prompt_cache_key": "support",
  "safety_identifier": "user-1"
}`

const systemOneAnswerJSON = `{
  "model": "jev-1.13.0",
  "answers": {
    "escalate": {"type": "noul", "noul": 0.93},
    "department": {"type": "choice", "choice": "billing", "confidence": 0.4,
                   "probabilities": {"billing": 0.6, "shipping": 0.3, "false": 0.1}},
    "question_3": {"type": "score", "score": 0.8, "confidence": 0.7,
                   "legend": {"0": "Calm", "1": {"label": "Angry", "description": "Raised voice"}},
                   "probabilities": {"0": 0.2, "1": 0.8}}
  },
  "usage": {"input_tokens": 101, "output_tokens": 12}
}`

func decide(t *testing.T, provider *Provider, body string) (*core.PassthroughResponse, error) {
	t.Helper()
	return provider.Decisions(context.Background(), &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "decisions",
		Model:        "jev-latest",
		Body:         io.NopCloser(strings.NewReader(body)),
		Headers:      http.Header{"Content-Type": {"application/json"}},
		ProviderName: "jev",
	})
}

// A Decisions request reaches System One as the equivalent questions, and
// the answer comes back in OpenAI's shapes: in question order, under the
// names sent (null for an unnamed question), with choice values typed as
// sent.
func TestDecisions_TranslatesToSystemOneAndBack(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, systemOneAnswerJSON)
	provider := newTestProvider("ts-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, decisionsRequest)
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := capture.Last(t)
	assert.Equal(t, "/v1/systemone", sent.Path)
	assert.Equal(t, "Bearer ts-key", sent.Header.Get("Authorization"))
	assert.JSONEq(t, `{
	  "model": "jev-latest",
	  "state": "Shoes arrived late.\n\nI was charged twice.",
	  "questions": {
	    "escalate": {"type": "noul", "instructions": "Does this need urgent attention?"},
	    "department": {"type": "choice", "instructions": "Which team?",
	                   "criteria": {"billing": "Charges", "shipping": null, "false": "Neither"}},
	    "question_3": {"type": "score", "instructions": "How frustrated?",
	                   "criteria": ["Calm", {"label": "Angry", "description": "Raised voice"}]}
	  }
	}`, string(sent.Body))

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", http.Header(resp.Headers).Get("Content-Type"))
	assert.Empty(t, http.Header(resp.Headers).Get("Content-Length"))
	assert.JSONEq(t, `{
	  "model": "jev-1.13.0",
	  "answers": [
	    {"name": "escalate", "type": "predicate", "probability": 0.93},
	    {"name": "department", "type": "choice", "choice": "billing", "confidence": 0.4,
	     "probabilities": [{"value": "billing", "probability": 0.6}, {"value": "shipping", "probability": 0.3}, {"value": false, "probability": 0.1}]},
	    {"name": null, "type": "score", "score": 0.8, "confidence": 0.7,
	     "probabilities": [{"label": "Calm", "probability": 0.2, "value": 0}, {"label": "Angry", "probability": 0.8, "value": 1}]}
	  ],
	  "usage": {"input_tokens": 101, "input_tokens_details": {"cache_write_tokens": 0, "cached_tokens": 0},
	            "output_tokens": 12, "output_tokens_details": {"reasoning_tokens": 0}, "total_tokens": 113}
	}`, string(got))
}

// A boolean choice comes back as the boolean the caller sent, not the text
// System One names it by.
func TestDecisions_BooleanChoiceKeepsItsType(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{"ok":{"type":"choice","choice":"true","confidence":0.9,"probabilities":{"true":0.95,"false":0.05}}},"usage":{"input_tokens":5,"output_tokens":1}}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"fine","questions":[{"type":"choice","name":"ok","instructions":"OK?","choices":[{"value":true},{"value":false}]}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"choice":true`)
	assert.Contains(t, string(got), `{"value":true,"probability":0.95}`)
}

// A question System One leaves unanswered, or answers with an unexpected
// shape, is a refusal: the Decisions API lets any question refuse while the
// others still answer.
func TestDecisions_UnansweredQuestionsAreRefusals(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{"a":{"type":"noul","noul":0.1},"b":{"type":"choice","choice":"unknown"}},"usage":{"input_tokens":5,"output_tokens":1}}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"x","questions":[
	  {"type":"predicate","name":"a","instructions":"A?"},
	  {"type":"choice","name":"b","instructions":"B?","choices":[{"value":"x"},{"value":"y"}]},
	  {"type":"predicate","name":"c","instructions":"C?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `[
	  {"name":"a","type":"predicate","probability":0.1},
	  {"name":"b","type":"refusal"},
	  {"name":"c","type":"refusal"}
	]`, extractJSONMember(t, got, "answers"))
}

// Requests System One cannot answer fail before reaching the upstream, as
// invalid requests naming what to change.
func TestDecisions_RejectsUntranslatableRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "image input", body: `{"model":"jev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}],"questions":[{"type":"predicate","instructions":"?"}]}`, want: "read text only"},
		{name: "missing input", body: `{"model":"jev-latest","questions":[{"type":"predicate","instructions":"?"}]}`, want: "input is required"},
		{name: "no questions", body: `{"model":"jev-latest","input":"x","questions":[]}`, want: "at least one question"},
		{name: "unknown question type", body: `{"model":"jev-latest","input":"x","questions":[{"type":"rank","instructions":"?"}]}`, want: `"rank" is not a decision question type`},
		{name: "duplicate name", body: `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","name":"a","instructions":"?"},{"type":"predicate","name":"a","instructions":"?"}]}`, want: "not unique"},
		{name: "string and boolean collide", body: `{"model":"jev-latest","input":"x","questions":[{"type":"choice","instructions":"?","choices":[{"value":"true"},{"value":true}]}]}`, want: "collide"},
		{name: "numeric choice", body: `{"model":"jev-latest","input":"x","questions":[{"type":"choice","instructions":"?","choices":[{"value":1},{"value":2}]}]}`, want: "string or a boolean"},
		{name: "malformed body", body: `{"model":`, want: "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
			provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

			_, err := decide(t, provider, tt.body)
			providertest.AssertUnsupported(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, capture.Count())
		})
	}
}

// An unnamed question takes a generated System One key that never shadows a
// name the caller chose.
func TestDecisions_UnnamedKeysAvoidCallerNames(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"x","questions":[
	  {"type":"predicate","instructions":"first"},
	  {"type":"predicate","name":"question_1","instructions":"second"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := string(capture.Last(t).Body)
	assert.Contains(t, sent, `"question_1_":{"type":"noul","instructions":"first"}`)
	assert.Contains(t, sent, `"question_1":{"type":"noul","instructions":"second"}`)
}

// Upstream errors are relayed untouched, so the gateway reports TypeSafe's
// status and message as it does on /v1/systemone.
func TestDecisions_RelaysUpstreamErrors(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnprocessableEntity, `{"detail":[{"msg":"field required"}]}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","instructions":"?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Contains(t, string(got), "field required")
}

// A string input becomes the state as is; message content given as a string
// counts as text.
func TestDecisions_StringInputs(t *testing.T) {
	for name, input := range map[string]string{
		"string":         `"hello"`,
		"string content": `[{"role":"user","content":"hello"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)
			provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := decide(t, provider, `{"model":"jev-latest","input":`+input+`,"questions":[{"type":"predicate","instructions":"?"}]}`)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, `"hello"`, extractJSONMember(t, capture.Last(t).Body, "state"))
		})
	}
}

func extractJSONMember(t *testing.T, body []byte, key string) string {
	t.Helper()
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &members))
	return string(members[key])
}

// System One reads choice options in the order sent, so the caller's order
// must survive translation, and the questions keep theirs too. A Go map would
// sort both alphabetically.
func TestDecisions_KeepsQuestionAndOptionOrder(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"x","questions":[
	  {"type":"choice","name":"zz_first","instructions":"?","choices":[{"value":"zebra"},{"value":"mango"},{"value":"apple","description":"fallback, keep last"}]},
	  {"type":"predicate","name":"aa_second","instructions":"?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, `{"model":"jev-latest","state":"x","questions":{`+
		`"zz_first":{"type":"choice","instructions":"?","criteria":{"zebra":null,"mango":null,"apple":"fallback, keep last"}},`+
		`"aa_second":{"type":"noul","instructions":"?"}}}`, string(capture.Last(t).Body))
}
