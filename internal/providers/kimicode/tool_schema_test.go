package kimicode

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// sanitizeCases pairs a tool's parameters with what Moonshot is sent. Every
// "want" was checked against Moonshot's published validator
// (github.com/MoonshotAI/walle, level lite) at v0.1.18, its current release,
// and at v0.1.14, the last one before the rules were relaxed.
var sanitizeCases = []struct {
	name string
	in   string
	want string
}{
	{
		name: "enum without a type gets the type of its values",
		in: `{"type":"object","properties":{
			"status":{"enum":["todo","done"]},
			"count":{"enum":[1,2]},
			"ratio":{"enum":[1,2.5]},
			"flag":{"enum":[true]},
			"nothing":{"enum":[null]}}}`,
		want: `{"type":"object","properties":{
			"status":{"type":"string","enum":["todo","done"]},
			"count":{"type":"integer","enum":[1,2]},
			"ratio":{"type":"number","enum":[1,2.5]},
			"flag":{"type":"boolean","enum":[true]},
			"nothing":{"type":"null","enum":[null]}}}`,
	},
	{
		name: "nullable enum gets a type paired with null",
		in:   `{"type":"object","properties":{"status":{"enum":["todo",null]},"count":{"enum":[1,null]}}}`,
		want: `{"type":"object","properties":{
			"status":{"type":["null","string"],"enum":["todo",null]},
			"count":{"type":["null","integer"],"enum":[1,null]}}}`,
	},
	{
		name: "enum of several types is split into typed branches",
		in:   `{"type":"object","properties":{"size":{"description":"d","enum":["auto",1,2,null]}}}`,
		want: `{"type":"object","properties":{"size":{"description":"d","anyOf":[
			{"type":"string","enum":["auto"]},
			{"type":"integer","enum":[1,2]},
			{"type":"null"}]}}}`,
	},
	{
		name: "enum of objects cannot share a type list with null",
		in:   `{"type":"object","properties":{"point":{"enum":[{"x":1},null]}}}`,
		want: `{"type":"object","properties":{"point":{"anyOf":[{"type":"object","enum":[{"x":1}]},{"type":"null"}]}}}`,
	},
	{
		name: "type list too wide for an enum is narrowed to the values",
		in:   `{"type":"object","properties":{"mode":{"type":["string","integer","null"],"enum":["a","b"]}}}`,
		want: `{"type":"object","properties":{"mode":{"type":"string","enum":["a","b"]}}}`,
	},
	{
		name: "enum values the declared type rules out are dropped",
		in: `{"type":"object","properties":{
			"mode":{"type":"string","enum":["a",null,1]},
			"level":{"type":"number","enum":[1,2.5,"high"]},
			"gone":{"type":"string","enum":[1,2]}}}`,
		want: `{"type":"object","properties":{
			"mode":{"type":"string","enum":["a"]},
			"level":{"type":"number","enum":[1,2.5]},
			"gone":{"type":"string"}}}`,
	},
	{
		name: "empty enum is dropped",
		in:   `{"type":"object","properties":{"mode":{"description":"d","enum":[]}}}`,
		want: `{"type":"object","properties":{"mode":{"description":"d"}}}`,
	},
	{
		name: "enum and type give way beside a reference",
		in: `{"type":"object","properties":{"color":{"$ref":"#/$defs/Color","type":"string","enum":["red"],"description":"d"}},
			"$defs":{"Color":{"type":"string"}}}`,
		want: `{"type":"object","properties":{"color":{"$ref":"#/$defs/Color","description":"d"}},
			"$defs":{"Color":{"type":"string"}}}`,
	},
	{
		name: "tuple items become one schema accepting any member",
		in: `{"type":"object","properties":{
			"pair":{"type":"array","items":[{"type":"string"},{"type":"integer"}]},
			"single":{"type":"array","items":[{"enum":["a"]}]},
			"none":{"type":"array","items":[]}}}`,
		want: `{"type":"object","properties":{
			"pair":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"integer"}]}},
			"single":{"type":"array","items":{"type":"string","enum":["a"]}},
			"none":{"type":"array"}}}`,
	},
	{
		name: "boolean schemas become their object equivalents",
		in: `{"type":"object","properties":{
			"any":true,
			"never":false,
			"list":{"type":"array","items":true},
			"either":{"anyOf":[{"type":"string"},true]},
			"ref":{"$ref":"#/$defs/Anything"}},
			"$defs":{"Anything":true},
			"additionalProperties":false}`,
		want: `{"type":"object","properties":{
			"any":{},
			"never":{"not":{}},
			"list":{"type":"array","items":{}},
			"either":{"anyOf":[{"type":"string"},{}]},
			"ref":{"$ref":"#/$defs/Anything"}},
			"$defs":{"Anything":{}},
			"additionalProperties":false}`,
	},
	{
		name: "type beside anyOf moves into the branches that declare none",
		in: `{"type":"object","properties":{"value":{"type":"string","description":"d","anyOf":[
			{"minLength":1},
			{"type":"null"},
			{"$ref":"#/$defs/Name"},
			{"enum":["a",1]}]}},
			"$defs":{"Name":{"type":"string"}}}`,
		want: `{"type":"object","properties":{"value":{"description":"d","anyOf":[
			{"type":"string","minLength":1},
			{"type":"null"},
			{"$ref":"#/$defs/Name"},
			{"type":"string","enum":["a"]}]}},
			"$defs":{"Name":{"type":"string"}}}`,
	},
	{
		name: "type beside anyOf at the root",
		in: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},
			"anyOf":[{"required":["a"]},{"required":["b"]}]}`,
		want: `{"properties":{"a":{"type":"string"},"b":{"type":"string"}},
			"anyOf":[{"type":"object"},{"type":"object"}]}`,
	},
	{
		name: "enum beside anyOf gives way to the branches",
		in:   `{"type":"object","properties":{"value":{"enum":["a"],"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
		want: `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
	},
	{
		name: "required names missing from properties are pruned",
		in: `{"type":"object","properties":{"a":{"type":"string"},
			"nested":{"type":"object","required":["x"]},
			"mixed":{"type":"object","properties":{"k":{"type":"string"}},"required":["k",7,"gone"]}},
			"required":["a","missing"]}`,
		want: `{"type":"object","properties":{"a":{"type":"string"},
			"nested":{"type":"object"},
			"mixed":{"type":"object","properties":{"k":{"type":"string"}},"required":["k"]}},
			"required":["a"]}`,
	},
	{
		name: "repairs reach every keyword that holds a schema",
		in: `{"type":"object",
			"properties":{"map":{"type":"object","additionalProperties":{"enum":["a"]}},
				"list":{"type":"array","items":{"type":"object","properties":{"k":{"enum":[1]}}}}},
			"$defs":{"Status":{"enum":["on","off"]}}}`,
		want: `{"type":"object",
			"properties":{"map":{"type":"object","additionalProperties":{"type":"string","enum":["a"]}},
				"list":{"type":"array","items":{"type":"object","properties":{"k":{"type":"integer","enum":[1]}}}}},
			"$defs":{"Status":{"type":"string","enum":["on","off"]}}}`,
	},
	{
		name: "properties named like keywords stay properties",
		in: `{"type":"object","properties":{
			"enum":{"type":"array","items":{"type":"string"}},
			"required":{"type":"boolean"},
			"items":{"type":"integer"},
			"type":{"enum":["a"]}},
			"required":["enum","required","items","type"]}`,
		want: `{"type":"object","properties":{
			"enum":{"type":"array","items":{"type":"string"}},
			"required":{"type":"boolean"},
			"items":{"type":"integer"},
			"type":{"type":"string","enum":["a"]}},
			"required":["enum","required","items","type"]}`,
	},
}

func decodeSchema(t *testing.T, raw string) map[string]any {
	t.Helper()
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &schema))
	return schema
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func TestSanitizeSchema(t *testing.T) {
	for _, tc := range sanitizeCases {
		t.Run(tc.name, func(t *testing.T) {
			schema := decodeSchema(t, tc.in)
			before := encodeJSON(t, schema)

			got, changed := sanitizeSchema(schema)

			assert.True(t, changed)
			assert.JSONEq(t, tc.want, encodeJSON(t, got))
			assert.JSONEq(t, before, encodeJSON(t, schema), "the caller's schema must not be mutated")

			_, again := sanitizeSchema(got)
			assert.False(t, again, "a repaired schema needs no further repair")
		})
	}
}

// Schemas the validator accepts are forwarded as they came, including the
// keywords MFJS does not support: Moonshot ignores those instead of failing.
func TestSanitizeSchema_LeavesAcceptedSchemasAlone(t *testing.T) {
	for name, raw := range map[string]string{
		"plain object":         `{"type":"object","properties":{"q":{"type":"string","description":"d"}},"required":["q"],"additionalProperties":false}`,
		"empty":                `{}`,
		"typed enums":          `{"type":"object","properties":{"a":{"type":"string","enum":["x"]},"b":{"type":["integer","null"],"enum":[1,null]},"c":{"type":"array","enum":[[1]]}}}`,
		"nullable union":       `{"type":"object","properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null,"title":"A"}}}`,
		"references":           `{"type":"object","properties":{"a":{"$ref":"#/$defs/A","description":"d"}},"$defs":{"A":{"type":"object","properties":{"next":{"$ref":"#/$defs/A"}}}}}`,
		"untyped property":     `{"type":"object","properties":{"a":{"description":"anything"},"b":{}}}`,
		"unsupported keywords": `{"type":"object","properties":{"a":{"oneOf":[{"type":"string"},{"const":1}]},"b":{"type":"string","format":"uri","pattern":"^a"},"c":{"const":"x"},"d":{"allOf":[{"type":"string"}]},"e":{"type":"array","prefixItems":[{"type":"string"}]}}}`,
		"malformed keywords":   `{"type":"object","properties":"nope","required":"a","enum":"x","anyOf":{},"items":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			schema := decodeSchema(t, raw)
			got, changed := sanitizeSchema(schema)
			assert.False(t, changed)
			assert.JSONEq(t, raw, encodeJSON(t, got))
		})
	}
}

func TestSanitizeTools(t *testing.T) {
	const invalid = `{"type":"object","properties":{"status":{"enum":["todo","done"]}}}`
	const valid = `{"type":"object","properties":{"status":{"type":"string","enum":["todo","done"]}}}`

	t.Run("repairs both tool shapes and leaves the rest alone", func(t *testing.T) {
		tools := []map[string]any{
			{"type": "function", "function": map[string]any{"name": "chat_shape", "parameters": decodeSchema(t, invalid)}},
			{"type": "function", "name": "responses_shape", "parameters": decodeSchema(t, invalid)},
			{"type": "function", "function": map[string]any{"name": "already_valid", "parameters": decodeSchema(t, valid)}},
			{"type": "function", "function": map[string]any{"name": "no_parameters"}},
			{"type": "web_search"},
		}
		before := encodeJSON(t, tools)

		got, changed := sanitizeTools(tools)

		require.True(t, changed)
		require.Len(t, got, len(tools))
		assert.JSONEq(t, valid, encodeJSON(t, got[0]["function"].(map[string]any)["parameters"]))
		assert.Equal(t, "chat_shape", got[0]["function"].(map[string]any)["name"])
		assert.JSONEq(t, valid, encodeJSON(t, got[1]["parameters"]))
		assert.Equal(t, "responses_shape", got[1]["name"])
		for i := 2; i < len(tools); i++ {
			assert.JSONEq(t, encodeJSON(t, tools[i]), encodeJSON(t, got[i]))
		}
		assert.JSONEq(t, before, encodeJSON(t, tools), "the caller's tools must not be mutated")
	})

	t.Run("reports no change when every schema is accepted", func(t *testing.T) {
		tools := []map[string]any{
			{"type": "function", "function": map[string]any{"name": "ok", "parameters": decodeSchema(t, valid)}},
		}
		_, changed := sanitizeTools(tools)
		assert.False(t, changed)

		_, changed = sanitizeTools(nil)
		assert.False(t, changed)
	})
}

// issueToolParameters is the schema from the report that prompted the
// sanitizer: Moonshot answered it with 400 "At path 'properties.status': type
// is not defined".
const issueToolParameters = `{"type":"object","properties":{"status":{"enum":["todo","in_progress","done"]}},"required":["status","assignee"]}`

const issueToolParametersSent = `{"type":"object","properties":{"status":{"type":"string","enum":["todo","in_progress","done"]}},"required":["status"]}`

func TestChatCompletion_SanitizesToolSchemas(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server)

	req := &core.ChatRequest{
		Model:    "kimi-for-coding",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools: []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "update_task", "parameters": decodeSchema(t, issueToolParameters)},
		}},
	}
	_, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)

	wire := capture.Last(t).JSON(t)
	tools, ok := wire["tools"].([]any)
	require.True(t, ok, "wire tools = %#v", wire["tools"])
	require.Len(t, tools, 1)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, "update_task", function["name"])
	assert.JSONEq(t, issueToolParametersSent, encodeJSON(t, function["parameters"]))
	assert.JSONEq(t, issueToolParameters, encodeJSON(t, req.Tools[0]["function"].(map[string]any)["parameters"]),
		"the caller's request must not be mutated")
}

func TestStreamChatCompletion_SanitizesToolSchemas(t *testing.T) {
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := newTestProvider(server)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "kimi-for-coding",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools: []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "update_task", "parameters": decodeSchema(t, issueToolParameters)},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	tools := capture.Last(t).JSON(t)["tools"].([]any)
	require.Len(t, tools, 1)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	assert.JSONEq(t, issueToolParametersSent, encodeJSON(t, function["parameters"]))
}

// The Responses API is forwarded natively, so its tools (the schema sits on
// the tool itself) are repaired on that path too.
func TestResponses_SanitizesToolSchemas(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesGoldenBody)
	provider := newTestProvider(server)

	req := &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: "hi",
		Tools: []map[string]any{{"type": "function", "name": "update_task", "parameters": decodeSchema(t, issueToolParameters)}},
	}
	_, err := provider.Responses(context.Background(), req)
	require.NoError(t, err)

	recorded := capture.Last(t)
	assert.Equal(t, "/responses", recorded.Path)
	tools, ok := recorded.JSON(t)["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "update_task", tool["name"])
	assert.JSONEq(t, issueToolParametersSent, encodeJSON(t, tool["parameters"]))
	assert.JSONEq(t, issueToolParameters, encodeJSON(t, req.Tools[0]["parameters"]), "the caller's request must not be mutated")
}

func TestAdaptRequests_ReturnTheRequestWhenToolsNeedNoRepair(t *testing.T) {
	tools := []map[string]any{{
		"type":     "function",
		"function": map[string]any{"name": "ok", "parameters": decodeSchema(t, issueToolParametersSent)},
	}}

	chat := &core.ChatRequest{Model: "kimi-for-coding", Tools: tools}
	adapted, err := adaptChatRequest(chat)
	require.NoError(t, err)
	assert.Same(t, chat, adapted)

	responses := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi", Tools: tools}
	assert.Same(t, responses, adaptResponsesRequest(responses))
}
