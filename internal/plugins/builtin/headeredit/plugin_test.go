package headeredit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

type fakeHost struct{}

func (fakeHost) Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (fakeHost) Inference() pluginapi.Inference {
	return nil
}
func (fakeHost) History(context.Context, pluginapi.Meta) ([]pluginapi.Message, error) {
	return nil, nil
}
func (fakeHost) Metrics() pluginapi.Metrics { return noopMetrics{} }

type noopMetrics struct{}

func (noopMetrics) Inc(string, map[string]string)              {}
func (noopMetrics) Observe(string, float64, map[string]string) {}

func newPlugin(t *testing.T, cfg string) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(context.Background(), json.RawMessage(cfg), fakeHost{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p.(*Plugin)
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.Name != "header_edit" || m.Mutates {
		t.Fatalf("manifest = %+v", m)
	}
	if !reflect.DeepEqual(m.Kinds, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse}) {
		t.Errorf("kinds = %v", m.Kinds)
	}
	want := []string{"request_set", "request_remove", "response_set", "response_add", "response_remove", "upstream_set"}
	var keys []string
	for _, f := range m.ConfigSchema {
		keys = append(keys, f.Key)
		if f.Label == "" || f.Help == "" || f.Input != pluginapi.InputTextarea {
			t.Errorf("field %s incomplete: %+v", f.Key, f)
		}
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if _, ok := New().(pluginapi.PromptHook); !ok {
		t.Error("plugin must implement PromptHook")
	}
	if _, ok := New().(pluginapi.ResponseHook); !ok {
		t.Error("plugin must implement ResponseHook")
	}
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
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), fakeHost{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
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
			if err := New().Init(context.Background(), json.RawMessage(tt.cfg), fakeHost{}); err != nil {
				t.Fatalf("Init: %v", err)
			}
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
	if err != nil || d.Action != pluginapi.ActionAllow {
		t.Fatalf("OnPrompt = %+v, %v", d, err)
	}
	wantReq := http.Header{"X-Team": {"platform"}, "X-Env": {"prod"}, "Accept": {"*/*"}}
	if !reflect.DeepEqual(x.Headers.Request, wantReq) {
		t.Errorf("request = %v, want %v", x.Headers.Request, wantReq)
	}
	wantResp := http.Header{"Cache-Control": {"no-store"}, "X-Served-By": {"upstream", "gomodel"}}
	if !reflect.DeepEqual(x.Headers.Response, wantResp) {
		t.Errorf("response = %v, want %v", x.Headers.Response, wantResp)
	}
	if got := x.Headers.Upstream.Get("X-Tenant"); got != "acme" {
		t.Errorf("upstream X-Tenant = %q", got)
	}
	wantDetail := map[string][]string{
		"request":  {"X-Team", "X-Env", "X-Debug"},
		"response": {"Cache-Control", "X-Served-By", "X-Internal"},
		"upstream": {"X-Tenant"},
	}
	if !reflect.DeepEqual(d.Detail, wantDetail) {
		t.Errorf("detail = %v, want %v", d.Detail, wantDetail)
	}
	if strings.Contains(mustJSON(t, d.Detail), "platform") {
		t.Error("detail must not contain header values")
	}
}

func TestIdempotentAcrossPhases(t *testing.T) {
	p := newPlugin(t, `{"response_add": "X-Served-By: gomodel", "response_set": "X-Mode: strict"}`)
	x := &pluginapi.Exchange{Headers: &pluginapi.Headers{}, Values: pluginapi.Values{}}
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	d, err := p.OnResponse(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Headers.Response.Values("X-Served-By"); !reflect.DeepEqual(got, []string{"gomodel"}) {
		t.Errorf("X-Served-By = %v, want added once", got)
	}
	if got := x.Headers.Response.Values("X-Mode"); !reflect.DeepEqual(got, []string{"strict"}) {
		t.Errorf("X-Mode = %v", got)
	}
	if detail := d.Detail.(map[string][]string); !reflect.DeepEqual(detail["response"], []string{"X-Mode"}) {
		t.Errorf("second-phase detail = %v, want only the set header", detail)
	}

	// Two instances do not share the add guard.
	other := newPlugin(t, `{"response_add": "X-Served-By: other"}`)
	if _, err := other.OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Headers.Response.Values("X-Served-By"); !reflect.DeepEqual(got, []string{"gomodel", "other"}) {
		t.Errorf("X-Served-By after second instance = %v", got)
	}
}

func TestNilHeadersAndValues(t *testing.T) {
	p := newPlugin(t, `{"request_set": "X-A: 1", "response_add": "X-B: 2", "upstream_set": "X-C: 3"}`)
	x := &pluginapi.Exchange{}
	d, err := p.OnResponse(context.Background(), x)
	if err != nil || d.Action != pluginapi.ActionAllow {
		t.Fatalf("OnResponse = %+v, %v", d, err)
	}
	if x.Headers == nil || x.Headers.Request.Get("X-A") != "1" || x.Headers.Response.Get("X-B") != "2" || x.Headers.Upstream.Get("X-C") != "3" {
		t.Errorf("headers = %+v", x.Headers)
	}
}

func TestNoEditsDetail(t *testing.T) {
	p := newPlugin(t, `{}`)
	x := &pluginapi.Exchange{Headers: &pluginapi.Headers{Request: http.Header{"A": {"1"}}}, Values: pluginapi.Values{}}
	d, err := p.OnPrompt(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Detail.(map[string][]string)) != 0 {
		t.Errorf("detail = %v, want empty", d.Detail)
	}
	if x.Headers.Response != nil || x.Headers.Upstream != nil {
		t.Errorf("unused header maps must stay nil: %+v", x.Headers)
	}
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
			if got := New().(*Plugin).Summarize(json.RawMessage(tt.cfg)); got != tt.want {
				t.Errorf("Summarize = %q, want %q", got, tt.want)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
