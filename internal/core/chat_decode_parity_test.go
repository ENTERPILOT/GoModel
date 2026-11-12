package core

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// chatDecodeParityInputs are bodies on which decoding in place could diverge
// from the reference decode: key matching, duplicates, nulls, wrong kinds,
// whitespace and malformed JSON at every nesting level.
var chatDecodeParityInputs = []string{
	``,
	` `,
	`null`,
	` null `,
	`{}`,
	`[]`,
	`"x"`,
	`5`,
	`{"choices":[],"messages":[]}`,
	`{"choices":null,"messages":null}`,
	`{"choices":[null],"messages":[null]}`,
	`{"choices":"x","messages":"x"}`,
	`{"choices":{},"messages":{}}`,
	`{"choices":5,"messages":true}`,
	`{"choices":[1],"messages":[1]}`,
	`{"choices":["x"],"messages":["x"]}`,
	`{"choices":[[]],"messages":[[]]}`,
	`{"choices":[{}],"messages":[{}]}`,
	`{"choices":[{"index":1}],"choices":[{"index":2},{"index":3}]}`,
	`{"choices":[{"index":1},{"index":2}],"choices":[]}`,
	`{"choices":[{"index":1}],"choices":null}`,
	`{"choices":null,"choices":[{"index":1}]}`,
	`{"choices":[{"index":1}],"choices":"x"}`,
	`{"choices":"x","choices":[{"index":1}]}`,
	`{"CHOICES":[{"index":3}],"Messages":[{"role":"user","content":"hi"}]}`,
	`{"choiceſ":[{"index":4}],"meſſages":[{"role":"user","content":"hi"}]}`,
	`{"choices":[{"index":5}],"messages":[{"role":"user","content":"hi"}]}`,
	`{"choices":[{"index":1}],"Choices":[{"index":2}]}`,
	`{"messages":[{"role":"user","content":"a"}],"MESSAGES":[{"role":"assistant","content":"b"}]}`,
	` { "choices" : [ { "message" : { "role" : "assistant" , "content" : "hi" } } ] , "messages" : [ { "role" : "user" , "content" : "hi" } ] } `,
	"\n\t{\"choices\":\n[\n{\"message\":{\"content\":\"x\"}}\n]\n}\n",
	`{"choices":[{"message":null}]}`,
	`{"choices":[{"message":"x"}]}`,
	`{"choices":[{"message":[]}]}`,
	`{"choices":[{"message":{}}]}`,
	`{"choices":[{"message":{"role":"assistant","content":"a"},"message":{"content":"b"}}]}`,
	`{"choices":[{"message":{"content":"a"},"Message":{"role":"assistant"}}]}`,
	`{"choices":[{"messagE":{"content":"a"},"x_extra":1}]}`,
	`{"choices":[{"message":{"content":"a"},"message":null}]}`,
	`{"choices":[{"message":{"content":"a"},"message":"x"}]}`,
	`{"choices":[{"message":{"content":null}}],"messages":[{"role":"user","content":null}]}`,
	`{"choices":[{"message":{"content":5}}],"messages":[{"role":"user","content":5}]}`,
	`{"choices":[{"message":{"content":{}}}],"messages":[{"role":"user","content":{}}]}`,
	`{"choices":[{"message":{"content":"a","content":"b"}}],"messages":[{"content":"a","content":"b"}]}`,
	`{"choices":[{"message":{"content":"a","Content":"b"}}],"messages":[{"content":"a","CONTENT":"b"}]}`,
	`{"choices":[{"message":{"content":"a","content":null}}],"messages":[{"content":"a","content":null}]}`,
	`{"choices":[{"message":{"content":"a","content":5}}],"messages":[{"content":"a","content":5}]}`,
	`{"choices":[{"message":{"content":5,"content":"a"}}],"messages":[{"content":5,"content":"a"}]}`,
	`{"choices":[{"message":{"conteNt":"a","x":{"y":[1,2]}}}],"messages":[{"conteNt":"a","x":{"y":[1,2]}}]}`,
	`{"choices":[{"message":{"content":[{"type":"text","text":"hi"}]}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]}]}`,
	`{"choices":[{"message":{"content":[{"type":"text"}]}}],"messages":[{"role":"user","content":[{"type":"bogus"}]}]}`,
	`{"choices":[{"message":{"content":[5]}}],"messages":[{"role":"user","content":[5]}]}`,
	`{"choices":[{"message":{"content":"é😀 \"q\" \\ \n"}}],"messages":[{"content":"é😀 \"q\" \\ \n"}]}`,
	`{"choices":[{"message":{"content":"\ud800"}}],"messages":[{"content":"\ud800"}]}`,
	"{\"choices\":[{\"message\":{\"content\":\"\xff\xfe\"}}],\"messages\":[{\"content\":\"\xff\xfe\"}]}",
	`{"choices":[{"message":{"role":5}}],"messages":[{"role":5}]}`,
	`{"choices":[{"message":{"tool_calls":"x"}}],"messages":[{"tool_calls":"x"}]}`,
	`{"choices":[{"message":{"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"},"x":1}]}}],"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
	`{"choices":[{"message":{"tool_calls":[{"function":5}]}}],"messages":[{"tool_calls":[{"function":5}]}]}`,
	`{"choices":[{"index":"x","message":{"content":"a"}}],"messages":[{"content":"a"}],"model":5}`,
	`{"model":5,"choices":[{"message":{"content":5}}],"messages":[{"content":5}]}`,
	`{"choices":[{"message":{"content":5}}],"messages":[{"content":5}],"model":5}`,
	`{"choices":[{"message":{"role":5,"content":5}}],"messages":[{"role":5,"content":5}],"temperature":"hot","created":"x"}`,
	`{"choices":[{"finish_reason":5,"message":{"role":5}}],"messages":[{"tool_call_id":5,"role":5}]}`,
	`{"choices":[{"logprobs":{"content":[]},"stop_sequence":"END","finish_reason":"stop","x_native":"y"}]}`,
	`{"choices":[{"logprobs":null}],"usage":null}`,
	`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"x":1},"usage":{"prompt_tokens":4}}`,
	`{"usage":"x"}`,
	`{"usage":5,"choices":[{"message":{"content":5}}]}`,
	`{"id":"a","object":"chat.completion","created":1,"model":"m","provider":"p","system_fingerprint":"fp","service_tier":"default","x":{"y":1},"choices":[{"index":0,"message":{"role":"assistant","content":"hi","refusal":null,"annotations":[]},"logprobs":null,"finish_reason":"stop"}]}`,
	`{"model":"m","provider":"p","temperature":0.2,"top_p":1,"max_tokens":10,"tools":[{"type":"function"}],"tool_choice":"auto","parallel_tool_calls":true,"stream":true,"stream_options":{"include_usage":true},"reasoning":{"effort":"low"},"user":"u","service_tier":"auto","x_extra":[1],"messages":[{"role":"user","content":"hi","name":"n"}]}`,
	`{"model":"m","messages":[{"role":"user","content":"hi"}],"x":1,"x":2}`,
	`{"choices":[{"message":{"content":"a"}}`,
	`{"choices":[{"message":{"content":"a"}}]`,
	`{"choices":[{"message":{"content":"a"}}]}}`,
	`{"choices":[{"message":{"content":"a"}}]} x`,
	`{"choices":[{"message":{"content":tru}}]}`,
	`{"choices":[{"message":{"content":"a\x"}}]}`,
	`{"choices":[{"message":{"content":01}}]}`,
	`{"choices":[{"message":{"content":"a",}}]}`,
	`{"choices":[{"message" {"content":"a"}}]}`,
	`{"choices":[{"message":{"content":"a"}},]}`,
	`{"messages":[{"role":"user","content":"a"}` + "\x00" + `]}`,
	`{"messages":[{"role":"user","content":"a` + "\x01" + `"}]}`,
	`{"messages":[{"role":"user","content":"a"}],}`,
	`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},]}]}`,
	`{"messages":[{"role":"user","content":1e999}]}`,
	`{"choices":[{"index":1e999}]}`,
	`{"choices":[{"index":99999999999999999999}]}`,
	`{"choices":[` + strings.Repeat(`[`, 200) + strings.Repeat(`]`, 200) + `]}`,
	`{"messages":[{"role":"user","content":` + strings.Repeat(`[`, 200) + strings.Repeat(`]`, 200) + `}]}`,
	`{"choices":[{"message":{"x":` + strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200) + `}}]}`,
}

// TestChatDecodeReferenceMirrorsTypes keeps the reference decoders honest:
// each mirrors the JSON members of the type it stands for.
func TestChatDecodeReferenceMirrorsTypes(t *testing.T) {
	tests := []struct {
		name string
		ref  any
		real any
	}{
		{name: "ChatRequest", ref: refChatRequestFields{}, real: ChatRequest{}},
		{name: "ChatResponse", ref: refChatResponseFields{}, real: ChatResponse{}},
		{name: "Choice", ref: refChoiceFields{}, real: Choice{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, jsonMembersOf(tt.real), jsonMembersOf(tt.ref))
		})
	}
}

// jsonMembersOf lists a struct's JSON members by tag, naming a ref decoder
// type after the type it stands for.
func jsonMembersOf(v any) []string {
	var members []string
	for field := range reflect.TypeOf(v).Fields() {
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		typ := strings.NewReplacer("core.ref", "core.").Replace(field.Type.String())
		members = append(members, tag+" "+typ)
	}
	return members
}

func TestChatDecodeMatchesReference(t *testing.T) {
	for _, body := range chatDecodeParityInputs {
		t.Run("", func(t *testing.T) {
			assertChatDecodeMatchesReference(t, []byte(body))
		})
	}
}

// TestChatDecodeMatchesReferenceOnFixtures decodes every JSON object in the
// recorded provider fixtures, at any depth, as each chat type.
func TestChatDecodeMatchesReferenceOnFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "contract", "testdata", "*", "*.json"))
	require.NoError(t, err)
	golden, err := filepath.Glob(filepath.Join("..", "..", "tests", "contract", "testdata", "golden", "*", "*.json"))
	require.NoError(t, err)
	files = append(files, golden...)
	require.NotEmpty(t, files)

	objects := 0
	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		forEachJSONObject(gjson.ParseBytes(body), func(raw string) {
			objects++
			assertChatDecodeMatchesReference(t, []byte(raw))
		})
	}
	assert.Greater(t, objects, 100)
}

func forEachJSONObject(value gjson.Result, visit func(raw string)) {
	if value.IsObject() {
		visit(value.Raw)
	}
	if value.IsObject() || value.IsArray() {
		value.ForEach(func(_, child gjson.Result) bool {
			forEachJSONObject(child, visit)
			return true
		})
	}
}

// TestChatDecodeDoesNotRetainInput decodes in place, so nothing decoded may
// alias the input: a caller may reuse its buffer once decoding returns.
func TestChatDecodeDoesNotRetainInput(t *testing.T) {
	body := `{"model":"m","x":"y","choices":[{"message":{"role":"assistant","content":"hello","x":"y"},"logprobs":{"a":1},"x":"y"}],` +
		`"messages":[{"role":"user","content":"hello","x":"y"},{"role":"user","content":[{"type":"text","text":"hi","x":"y"}]}]}`

	decode := func(data []byte) (ChatResponse, ChatRequest) {
		var resp ChatResponse
		require.NoError(t, json.Unmarshal(data, &resp))
		var req ChatRequest
		require.NoError(t, req.UnmarshalJSON(data))
		return resp, req
	}
	wantResp, wantReq := decode([]byte(body))

	data := []byte(body)
	gotResp, gotReq := decode(data)
	for i := range data {
		data[i] = 'z'
	}
	assert.Equal(t, wantResp, gotResp)
	assert.Equal(t, wantReq, gotReq)
}

func FuzzChatDecodeMatchesReference(f *testing.F) {
	for _, body := range chatDecodeParityInputs {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		assertChatDecodeMatchesReference(t, data)
	})
}

// assertChatDecodeMatchesReference decodes data as every chat type that
// decodes nested members in place, through json.Unmarshal and through a
// direct UnmarshalJSON call, and requires the reference decode's value and
// error.
func assertChatDecodeMatchesReference(t *testing.T, data []byte) {
	t.Helper()
	original := bytes.Clone(data)

	_, _ = assertSameDecode(t, data, &ChatResponse{}, &refChatResponse{}, func(r *refChatResponse) any { return &r.v })
	_, _ = assertSameDecode(t, data, &Choice{}, &refChoice{}, func(r *refChoice) any { return &r.v })
	_, _ = assertSameDecode(t, data, &ResponseMessage{}, &refResponseMessage{}, func(r *refResponseMessage) any { return &r.v })
	_, _ = assertSameDecode(t, data, &Message{}, &refMessage{}, func(r *refMessage) any { return &r.v })
	gotErr, wantErr := assertSameDecode(t, data, &ChatRequest{}, &refChatRequest{}, func(r *refChatRequest) any { return &r.v })
	if gotErr != nil && wantErr != nil {
		got := NewInvalidRequestBodyError(data, gotErr)
		want := NewInvalidRequestBodyError(data, wantErr)
		assert.Equal(t, want.Message, got.Message)
		assert.Equal(t, want.Param, got.Param)
	}

	// The reference decode is only the error path: whatever it accepts, the
	// in-place decode must accept on its own.
	if json.Unmarshal(data, &refChatResponse{}) == nil {
		require.NoError(t, new(ChatResponse).unmarshalJSONInPlace(data), "in place: %q", data)
	}
	if json.Unmarshal(data, &refChoice{}) == nil {
		require.NoError(t, new(Choice).unmarshalJSONInPlace(data), "in place: %q", data)
	}
	if json.Unmarshal(data, &refChatRequest{}) == nil {
		require.NoError(t, new(ChatRequest).unmarshalJSONInPlace(data), "in place: %q", data)
	}

	assert.Equal(t, original, data, "decoding must not modify its input")
}

func assertSameDecode[G any, R any, PR interface {
	*R
	json.Unmarshaler
}](t *testing.T, data []byte, got *G, want PR, value func(PR) any) (gotErr, wantErr error) {
	t.Helper()
	// Through json.Unmarshal, as providers and handlers decode.
	gotErr = json.Unmarshal(data, got)
	wantErr = json.Unmarshal(data, want)
	assertSameResult(t, data, got, value(want), gotErr, wantErr)

	// Directly, as the canonical request decode calls it.
	directGot := new(G)
	directWant := PR(new(R))
	directGotErr := any(directGot).(json.Unmarshaler).UnmarshalJSON(data)
	directWantErr := directWant.UnmarshalJSON(data)
	assertSameResult(t, data, directGot, value(directWant), directGotErr, directWantErr)
	return gotErr, wantErr
}

func assertSameResult(t *testing.T, data []byte, got, want any, gotErr, wantErr error) {
	t.Helper()
	if wantErr != nil {
		require.Error(t, gotErr, "%T must reject %q: %v", got, data, wantErr)
		// The reference types' names stand in for the real ones.
		wantText := strings.ReplaceAll(wantErr.Error(), "core.ref", "core.")
		assert.Equal(t, wantText, gotErr.Error(), "%T error for %q", got, data)
		assert.Equal(t, reflect.TypeOf(wantErr), reflect.TypeOf(gotErr), "%T error type for %q", got, data)
		return
	}
	require.NoError(t, gotErr, "%T must accept %q", got, data)
	assert.Equal(t, want, got, "%T value for %q", got, data)
}
