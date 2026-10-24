package systemone

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
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

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

// fakeUpstream stands in for a provider's Passthrough: it records the System
// One request it is sent and answers with a fixed status and body.
type fakeUpstream struct {
	status int
	answer string
	calls  int
	sent   *core.PassthroughRequest
	body   []byte
}

func upstream(status int, answer string) *fakeUpstream {
	return &fakeUpstream{status: status, answer: answer}
}

func (u *fakeUpstream) send(_ context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	u.calls++
	u.sent = req
	u.body, _ = io.ReadAll(req.Body)
	return &core.PassthroughResponse{
		StatusCode: u.status,
		Headers:    map[string][]string{"Content-Type": {"application/json"}, "Content-Length": {"999"}},
		Body:       io.NopCloser(strings.NewReader(u.answer)),
	}, nil
}

func decide(t *testing.T, up *fakeUpstream, body string) (*core.PassthroughResponse, error) {
	t.Helper()
	return decideWith(t, up, body, Options{ProviderType: "test"})
}

func decideWith(t *testing.T, up *fakeUpstream, body string, opts Options) (*core.PassthroughResponse, error) {
	t.Helper()
	return AnswerDecisions(context.Background(), &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "decisions",
		Model:        "jev-latest",
		Body:         io.NopCloser(strings.NewReader(body)),
		Headers:      http.Header{"Content-Type": {"application/json"}},
		ProviderName: "jev",
	}, up.send, opts)
}

// A Decisions request reaches System One as the equivalent questions, and
// the answer comes back in OpenAI's shapes: in question order, under the
// names sent (null for an unnamed question), with choice values typed as
// sent.
func TestDecisions_TranslatesToSystemOneAndBack(t *testing.T) {
	up := upstream(http.StatusOK, systemOneAnswerJSON)

	resp, err := decide(t, up, decisionsRequest)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "systemone", up.sent.Endpoint)
	assert.Equal(t, http.MethodPost, up.sent.Method)
	assert.Equal(t, "jev", up.sent.ProviderName)
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
	}`, string(up.body))

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
	up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{"ok":{"type":"choice","choice":"true","confidence":0.9,"probabilities":{"true":0.95,"false":0.05}}},"usage":{"input_tokens":5,"output_tokens":1}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"fine","questions":[{"type":"choice","name":"ok","instructions":"OK?","choices":[{"value":true},{"value":false}]}]}`)
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
	up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{"a":{"type":"noul","noul":0.1},"b":{"type":"choice","choice":"unknown"}},"usage":{"input_tokens":5,"output_tokens":1}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"x","questions":[
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
		{name: "image link", body: `{"model":"jev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}],"questions":[{"type":"predicate","instructions":"?"}]}`, want: "not a link"},
		{name: "webp image", body: `{"model":"jev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/webp;base64,UklGRg=="}]}],"questions":[{"type":"predicate","instructions":"?"}]}`, want: "PNG and JPEG"},
		{name: "image not base64", body: `{"model":"jev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png,raw"}]}],"questions":[{"type":"predicate","instructions":"?"}]}`, want: "base64 data URL"},
		{name: "file part", body: `{"model":"jev-latest","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-1"}]}],"questions":[{"type":"predicate","instructions":"?"}]}`, want: "read text and images only"},
		{name: "missing input", body: `{"model":"jev-latest","questions":[{"type":"predicate","instructions":"?"}]}`, want: "input is required"},
		{name: "no questions", body: `{"model":"jev-latest","input":"x","questions":[]}`, want: "at least one question"},
		{name: "unknown question type", body: `{"model":"jev-latest","input":"x","questions":[{"type":"rank","instructions":"?"}]}`, want: `"rank" is not a decision question type`},
		{name: "duplicate name", body: `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","name":"a","instructions":"?"},{"type":"predicate","name":"a","instructions":"?"}]}`, want: "not unique"},
		{name: "string and boolean collide", body: `{"model":"jev-latest","input":"x","questions":[{"type":"choice","instructions":"?","choices":[{"value":"true"},{"value":true}]}]}`, want: "collide"},
		{name: "null choice", body: `{"model":"jev-latest","input":"x","questions":[{"type":"choice","instructions":"?","choices":[{"value":null},{"value":"a"}]}]}`, want: "not null"},
		{name: "numeric choice", body: `{"model":"jev-latest","input":"x","questions":[{"type":"choice","instructions":"?","choices":[{"value":1},{"value":2}]}]}`, want: "string or a boolean"},
		{name: "malformed body", body: `{"model":`, want: "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := upstream(http.StatusOK, `{}`)

			_, err := decide(t, up, tt.body)
			providertest.AssertUnsupported(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, up.calls)
		})
	}
}

// An unnamed question takes a generated System One key that never shadows a
// name the caller chose.
func TestDecisions_UnnamedKeysAvoidCallerNames(t *testing.T) {
	up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"x","questions":[
	  {"type":"predicate","instructions":"first"},
	  {"type":"predicate","name":"question_1","instructions":"second"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := string(up.body)
	assert.Contains(t, sent, `"question_1_":{"type":"noul","instructions":"first"}`)
	assert.Contains(t, sent, `"question_1":{"type":"noul","instructions":"second"}`)
}

// Upstream errors are relayed untouched, so the gateway reports TypeSafe's
// status and message as it does on /v1/systemone.
func TestDecisions_RelaysUpstreamErrors(t *testing.T) {
	up := upstream(http.StatusUnprocessableEntity, `{"detail":[{"msg":"field required"}]}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","instructions":"?"}]}`)
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
			up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)

			resp, err := decide(t, up, `{"model":"jev-latest","input":`+input+`,"questions":[{"type":"predicate","instructions":"?"}]}`)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, `"hello"`, extractJSONMember(t, up.body, "state"))
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
	up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"x","questions":[
	  {"type":"choice","name":"zz_first","instructions":"?","choices":[{"value":"zebra"},{"value":"mango"},{"value":"apple","description":"fallback, keep last"}]},
	  {"type":"predicate","name":"aa_second","instructions":"?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, `{"model":"jev-latest","state":"x","questions":{`+
		`"zz_first":{"type":"choice","instructions":"?","criteria":{"zebra":null,"mango":null,"apple":"fallback, keep last"}},`+
		`"aa_second":{"type":"noul","instructions":"?"}}}`, string(up.body))
}

// Images become System One image assets: the state lists the text parts and
// image references in input order, each image is listed once in assets under
// its reference, and the request opts in to images with TypeSafe's header.
func TestDecisions_TranslatesImagesToAssets(t *testing.T) {
	up := upstream(http.StatusOK, `{"model":"jev-1.14.0","answers":{"damaged":{"type":"noul","noul":0.9}},"usage":{"input_tokens":900,"output_tokens":1}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":[{"role":"user","content":[
	  {"type":"input_text","text":"Delivery photo:"},
	  {"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo=","detail":"high"},
	  {"type":"input_image","image_url":"data:image/jpeg;base64,/9j/4AAQ"}]}],
	  "questions":[{"type":"predicate","name":"damaged","instructions":"Is the box damaged?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "true", up.sent.Headers.Get("TypeSafe-Danger-Zone-Images"))
	var body struct {
		State  []string `json:"state"`
		Assets map[string]struct {
			Type       string `json:"type"`
			Format     string `json:"format"`
			DataBase64 string `json:"data_base64"`
		} `json:"assets"`
	}
	require.NoError(t, json.Unmarshal(up.body, &body))
	require.Len(t, body.State, 3)
	assert.Equal(t, "Delivery photo:", body.State[0])
	require.Len(t, body.Assets, 2)
	png, jpg := body.Assets[body.State[1]], body.Assets[body.State[2]]
	assert.Equal(t, "image", png.Type)
	assert.Equal(t, "png", png.Format)
	assert.Equal(t, "iVBORw0KGgo=", png.DataBase64)
	assert.Equal(t, "jpg", jpg.Format)
	assert.NotEqual(t, body.State[1], body.State[2], "each image has its own reference")

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"probability":0.9`)
}

// Text-only requests keep a plain string state and send no image opt-in.
func TestDecisions_TextOnlyRequestsSendNoImageHeader(t *testing.T) {
	up := upstream(http.StatusOK, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)

	resp, err := decide(t, up, `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","instructions":"?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, up.sent.Headers.Get("TypeSafe-Danger-Zone-Images"))
	assert.NotContains(t, string(up.body), "assets")
}
