package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceMarshalWithUnknownJSONFields is the encoder that
// marshalMergingUnknownJSONObject replaced: marshal the known members, then
// copy them and the extras into a merged object. Upstream request bodies and
// cache keys depend on these bytes, so the replacement must match it exactly.
func referenceMarshalWithUnknownJSONFields(base any, extraBody []byte) ([]byte, error) {
	baseBody, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	return mergeUnknownJSONObject(baseBody, extraBody)
}

func assertSameMarshal(t *testing.T, base any, extraBody []byte) {
	t.Helper()
	want, wantErr := referenceMarshalWithUnknownJSONFields(base, extraBody)
	got, gotErr := marshalMergingUnknownJSONObject(base, extraBody)
	if wantErr != nil {
		assert.Error(t, gotErr)
		return
	}
	require.NoError(t, gotErr)
	assert.Equal(t, string(want), string(got))
}

func TestMarshalMergingUnknownJSONObjectMatchesReference(t *testing.T) {
	type known struct {
		Name   string          `json:"name,omitempty"`
		Raw    json.RawMessage `json:"raw,omitempty"`
		Float  *float64        `json:"float,omitempty"`
		Any    any             `json:"any,omitempty"`
		List   []string        `json:"list"`
		Nested *known          `json:"nested,omitempty"`
	}
	tiny := 1e-7
	huge := 1e21
	bases := map[string]any{
		"empty":        known{},
		"html":         known{Name: `<script>&amp;</script>`},
		"line sep":     known{Name: "a b c"},
		"invalid utf8": known{Name: "bad\xff\xfeend"},
		"control":      known{Name: "tab\tnl\nnul\x00"},
		"raw":          known{Raw: json.RawMessage(` { "a" : [ 1 , 2 ] , "h" : "<&>" } `)},
		"raw number":   known{Raw: json.RawMessage(`123456789012345678901234567890`)},
		"floats":       known{Float: &tiny, Any: huge},
		"nested":       known{Nested: &known{Name: "x", List: []string{}}},
		"map":          map[string]any{"b": 1, "a": "<", "c": nil},
		"all omitted": struct {
			A string `json:"a,omitempty"`
		}{},
		"null":       nil,
		"not object": []int{1, 2},
	}
	extras := map[string]string{
		"object":      `{"x":1,"y":"<&>"}`,
		"padded":      " \n {\"x\":[1, 2]} \t",
		"empty":       `{}`,
		"padded void": `  {}  `,
		"repeated":    `{"name":"dup","name":"again"}`,
		"unicode":     `{"ż":"😀","bad":"\ud800"}`,
		"array":       `[1]`,
		"string":      `"x"`,
		"blank":       `   `,
	}
	for baseName, base := range bases {
		for extraName, extra := range extras {
			t.Run(baseName+"/"+extraName, func(t *testing.T) {
				assertSameMarshal(t, base, []byte(extra))
			})
		}
	}
}

// TestChatMarshalMatchesReferenceOnFixtures decodes every JSON fixture in the
// repository as a chat request and as a chat response, and checks each object
// level against the reference encoder. Members a type does not model land in
// its ExtraFields, so the fixtures exercise extras at every level. Equal
// levels imply equal documents: a parent embeds its children's MarshalJSON
// output unchanged.
func TestChatMarshalMatchesReferenceOnFixtures(t *testing.T) {
	bodies := map[string][]byte{
		"chat request":         benchmarkChatRequestPayload,
		"multimodal request":   benchmarkMultimodalChatPayload,
		"responses request":    benchmarkResponsesRequestPayload,
		"chat response":        benchmarkChatResponseBody(200),
		"conversation request": benchmarkChatRequestBody(4, 120),
	}
	root := filepath.Join("..", "..", "tests", "contract", "testdata")
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		bodies[path] = body
		return nil
	}))
	require.Greater(t, len(bodies), 50)

	levels := 0
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			var req ChatRequest
			if json.Unmarshal(body, &req) == nil {
				levels += assertChatRequestLevels(t, req)
			}
			var resp ChatResponse
			if json.Unmarshal(body, &resp) == nil {
				levels += assertChatResponseLevels(t, resp)
			}
		})
	}
	assert.Greater(t, levels, 50, "fixtures must reach levels with extras")
}

func assertChatRequestLevels(t *testing.T, req ChatRequest) int {
	known := req
	known.ExtraFields = UnknownJSONFields{}
	levels := assertLevel(t, req, known, req.ExtraFields)
	for _, m := range req.Messages {
		known := m
		known.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, m, known, m.ExtraFields)
		levels += assertContentLevels(t, m.Content)
		levels += assertToolCallLevels(t, m.ToolCalls)
	}
	return levels
}

func assertChatResponseLevels(t *testing.T, resp ChatResponse) int {
	known := resp
	known.ExtraFields = UnknownJSONFields{}
	levels := assertLevel(t, resp, known, resp.ExtraFields)
	for _, c := range resp.Choices {
		knownChoice := c
		knownChoice.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, c, knownChoice, c.ExtraFields)
		m := c.Message
		knownMessage := m
		knownMessage.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, m, knownMessage, m.ExtraFields)
		levels += assertContentLevels(t, m.Content)
		levels += assertToolCallLevels(t, m.ToolCalls)
	}
	return levels
}

func assertContentLevels(t *testing.T, content MessageContent) int {
	parts, _ := content.([]ContentPart)
	levels := 0
	for _, p := range parts {
		known := p
		known.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, p, known, p.ExtraFields)
		if p.ImageURL != nil {
			known := *p.ImageURL
			known.ExtraFields = UnknownJSONFields{}
			levels += assertLevel(t, *p.ImageURL, known, p.ImageURL.ExtraFields)
		}
		if p.VideoURL != nil {
			known := *p.VideoURL
			known.ExtraFields = UnknownJSONFields{}
			levels += assertLevel(t, *p.VideoURL, known, p.VideoURL.ExtraFields)
		}
		if p.InputAudio != nil {
			known := *p.InputAudio
			known.ExtraFields = UnknownJSONFields{}
			levels += assertLevel(t, *p.InputAudio, known, p.InputAudio.ExtraFields)
		}
		if p.File != nil {
			known := *p.File
			known.ExtraFields = UnknownJSONFields{}
			levels += assertLevel(t, *p.File, known, p.File.ExtraFields)
		}
	}
	return levels
}

func assertToolCallLevels(t *testing.T, calls []ToolCall) int {
	levels := 0
	for _, call := range calls {
		known := call
		known.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, call, known, call.ExtraFields)
		knownFunction := call.Function
		knownFunction.ExtraFields = UnknownJSONFields{}
		levels += assertLevel(t, call.Function, knownFunction, call.Function.ExtraFields)
	}
	return levels
}

// assertLevel checks one object level that carries extras, the only case the
// new encoder handles, and reports whether it did. Without extras a level's
// MarshalJSON is plain json.Marshal of its known members, as before the
// change, so the reference output is that merged with the extras. Children
// are encoded by the same MarshalJSON methods on both sides; each child level
// is checked by its own call.
func assertLevel[T json.Marshaler](t *testing.T, v, known T, extra UnknownJSONFields) int {
	t.Helper()
	if extra.IsEmpty() {
		return 0
	}
	got, err := v.MarshalJSON()
	require.NoError(t, err)
	baseBody, err := known.MarshalJSON()
	require.NoError(t, err)
	want, err := mergeUnknownJSONObject(baseBody, extra.raw)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
	return 1
}

// FuzzMarshalMergingUnknownJSONObject checks the new encoder against the
// reference for arbitrary strings, raw values and extras.
func FuzzMarshalMergingUnknownJSONObject(f *testing.F) {
	f.Add("hello", []byte(`{"a":1}`), []byte(`{"x":"<&>"}`))
	f.Add("bad\xff", []byte(`[1, 2]`), []byte(" {} "))
	f.Add("", []byte(`null`), []byte(`[1]`))
	f.Add(" <", []byte(`1e400`), []byte(`{"a":"\ud800"}`))
	f.Fuzz(func(t *testing.T, s string, raw, extra []byte) {
		base := struct {
			S   string          `json:"s"`
			Raw json.RawMessage `json:"raw,omitempty"`
		}{S: s}
		if json.Valid(raw) {
			base.Raw = raw
		}
		assertSameMarshal(t, base, extra)
		assertSameMarshal(t, map[string]string{s: s}, extra)
	})
}
