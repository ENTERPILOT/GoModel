package headeredit

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPlugin(t *testing.T, cfg string) *Plugin {
	t.Helper()
	p := New()
	err := p.Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost())
	require.NoError(t, err)

	return p.(*Plugin)
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	require.Equal(t, "header_edit", m.Name)
	require.False(t, m.Mutates)
	require.True(t, m.Guardrail)
	assert.Equal(t, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse}, m.Kinds)

	want := []string{"request_set", "request_remove", "response_set", "response_add", "response_remove", "upstream_set"}
	var keys []string
	for _, f := range m.ConfigSchema {
		keys = append(keys, f.Key)
		assert.NotEmpty(t, f.Label)
		assert.NotEmpty(t, f.Help)
		assert.Equal(t, pluginapi.InputTextarea, f.Input, "field %s incomplete: %+v", f.Key, f)
	}
	assert.Equal(t, want, keys)
	_, ok := New().(pluginapi.PromptHook)
	assert.True(t, ok)
	_, ok = New().(pluginapi.ResponseHook)
	assert.True(t, ok)
}

func TestInitErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"unknown key", `{"bogus": "x"}`, `unknown field "bogus"`},
		{"malformed json", `{"request_set": `, "invalid config"},
		{"wrong type", `{"request_set": 5}`, "request_set must be text or a list of strings"},
		{"set without colon", `{"request_set": "X-Team"}`, `request_set line 1: expected "Name: value"`},
		{"remove with colon", `{"response_remove": "X-Team: a"}`, "response_remove line 1: expected a bare header name"},
		{"invalid name", `{"response_set": "X Team: a"}`, `invalid header name "X Team"`},
		{"empty name", `{"response_set": ": a"}`, "empty header name"},
		{"authorization", `{"request_set": "Authorization: Bearer x"}`, `header "Authorization" carries credentials`},
		{"cookie remove", `{"request_remove": "Cookie"}`, `header "Cookie" carries credentials`},
		{"set-cookie add", `{"response_add": "Set-Cookie: a=b"}`, `header "Set-Cookie" carries credentials`},
		{"api key case insensitive", `{"upstream_set": "X-API-KEY: k"}`, `header "X-API-KEY" carries credentials`},
		{"token-ish set", `{"upstream_set": "X-Session-Token: k"}`, `header "X-Session-Token" looks like a credential`},
		{"secret-ish add", `{"response_add": "X-Client-Secret: k"}`, `looks like a credential`},
		{"line number", `{"request_set": "X-A: 1\n\n# comment\nbroken"}`, "request_set line 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), plugintest.NewHost())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestInitAccepts(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
	}{
		{"empty", ``},
		{"null", `null`},
		{"empty object", `{}`},
		{"comments and blanks", `{"request_set": "# note\n\n  X-A: 1  \n"}`},
		{"array form", `{"request_remove": ["X-A", "X-B"]}`},
		{"token-ish remove allowed", `{"request_remove": "X-Session-Token"}`},
		{"value with colon", `{"response_set": "X-Time: 12:30"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), plugintest.NewHost())
			require.NoError(t, err)
		})
	}
}

func TestApplyEdits(t *testing.T) {
	p := newPlugin(t, `{
		"request_set": "X-Team: platform\nX-Env: prod",
		"request_remove": "X-Debug\nX-Missing",
		"response_set": "Cache-Control: no-store",
		"response_add": "X-Served-By: gomodel",
		"response_remove": "X-Internal",
		"upstream_set": "X-Tenant: acme"
	}`)
	x := &pluginapi.Exchange{
		Headers: &pluginapi.Headers{
			Request:  http.Header{"X-Debug": {"1"}, "X-Env": {"dev"}, "Accept": {"*/*"}},
			Response: http.Header{"X-Internal": {"1"}, "X-Served-By": {"upstream"}},
		},
		Values: pluginapi.Values{},
	}
	d, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)

	wantReq := http.Header{"X-Team": {"platform"}, "X-Env": {"prod"}, "Accept": {"*/*"}}
	assert.Equal(t, wantReq, x.Headers.Request)

	wantResp := http.Header{"Cache-Control": {"no-store"}, "X-Served-By": {"upstream", "gomodel"}, "X-Internal": {""}}
	assert.Equal(t, wantResp, x.Headers.Response)
	got := x.Headers.Upstream.Get("X-Tenant")
	assert.Equal(t, "acme", got)

	wantDetail := map[string][]string{
		"request":  {"X-Team", "X-Env", "X-Debug"},
		"response": {"Cache-Control", "X-Served-By", "X-Internal"},
		"upstream": {"X-Tenant"},
	}
	assert.Equal(t, wantDetail, d.Detail)
	assert.NotContains(t, mustJSON(t, d.Detail), "platform")
}

func TestIdempotentAcrossPhases(t *testing.T) {
	p := newPlugin(t, `{"response_add": "X-Served-By: gomodel", "response_set": "X-Mode: strict"}`)
	x := &pluginapi.Exchange{Headers: &pluginapi.Headers{}, Values: pluginapi.Values{}}
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	d, err := p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	got := x.Headers.Response.Values("X-Served-By")
	assert.Equal(t, []string{"gomodel"}, got)
	got = x.Headers.Response.Values("X-Mode")
	assert.Equal(t, []string{"strict"}, got)
	detail := d.Detail.(map[string][]string)
	assert.Equal(t, []string{"X-Mode"}, detail["response"], "second-phase detail = %v, want only the set header", detail)

	// Two instances do not share the add guard.
	other := newPlugin(t, `{"response_add": "X-Served-By: other"}`)
	_, err = other.OnResponse(context.Background(), x)
	require.NoError(t, err)
	got = x.Headers.Response.Values("X-Served-By")
	assert.Equal(t, []string{"gomodel", "other"}, got)
}

func TestNilHeadersAndValues(t *testing.T) {
	p := newPlugin(t, `{"request_set": "X-A: 1", "response_add": "X-B: 2", "upstream_set": "X-C: 3"}`)
	x := &pluginapi.Exchange{}
	d, err := p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)
	assert.NotNil(t, x.Headers)
	assert.Equal(t, "1", x.Headers.Request.Get("X-A"))
	assert.Equal(t, "2", x.Headers.Response.Get("X-B"))
	assert.Equal(t, "3", x.Headers.Upstream.Get("X-C"))
}

func TestNoEditsDetail(t *testing.T) {
	p := newPlugin(t, `{}`)
	x := &pluginapi.Exchange{Headers: &pluginapi.Headers{Request: http.Header{"A": {"1"}}}, Values: pluginapi.Values{}}
	d, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	assert.Empty(t, d.Detail.(map[string][]string))
	assert.Nil(t, x.Headers.Response)
	assert.Nil(t, x.Headers.Upstream, "unused header maps must stay nil: %+v", x.Headers)
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"empty", `{}`, "no header edits"},
		{"mixed", `{"request_set": "X-A: 1\nX-B: 2", "request_remove": "X-C", "response_add": "X-D: 4", "upstream_set": "X-E: 5"}`,
			"request: set X-A, X-B, remove X-C; response: add X-D; upstream: set X-E"},
		{"invalid", `{"request_set": "broken"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New().(*Plugin).Summarize(json.RawMessage(tt.cfg))
			assert.Equal(t, tt.want, got)
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)

	return string(b)
}
