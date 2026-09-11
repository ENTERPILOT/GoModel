package pluginapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var testSchema = []Field{
	{Key: "name"}, {Key: "mode"}, {Key: "flag"}, {Key: "n"}, {Key: "f"}, {Key: "opt"},
	{Key: "status"}, {Key: "list"}, {Key: "lines"}, {Key: "roles"},
}

func parse(t *testing.T, raw string) *Config {
	t.Helper()
	c, err := ParseConfig("demo", testSchema, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseConfig(%s): %v", raw, err)
	}
	return c
}

func TestParseConfigRejectsUnknownAndInvalid(t *testing.T) {
	for raw, want := range map[string]string{
		`{"bogus": 1}`: `demo: invalid config: unknown field "bogus"`,
		`{"name": `:    "demo: invalid config:",
		`[1]`:          "demo: invalid config:",
	} {
		if _, err := ParseConfig("demo", testSchema, json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
	for _, raw := range []string{``, `  `, `null`, `{}`} {
		if c, err := ParseConfig("demo", testSchema, json.RawMessage(raw)); err != nil || c.String("name", "d") != "d" {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func TestConfigReaders(t *testing.T) {
	c := parse(t, `{"name": "x", "mode": "b", "flag": "yes", "n": "42", "f": 0.5, "opt": "0.25", "status": 451, "list": "a, b\n c,,", "lines": "one\n\n# two", "roles": "system, USER"}`)
	if c.String("name", "") != "x" || c.String("missing", "d") != "d" {
		t.Error("String")
	}
	if c.Choice("mode", "a", "a", "b") != "b" || c.Choice("missing", "a", "a", "b") != "a" {
		t.Error("Choice")
	}
	if !c.Bool("flag") || c.Bool("missing") {
		t.Error("Bool")
	}
	for raw, want := range map[string]bool{`true`: true, `false`: false, `"on"`: true, `"off"`: false, `"1"`: true, `"0"`: false, `"YES"`: true, `"no"`: false, `""`: false} {
		if got := parse(t, `{"flag": `+raw+`}`).Bool("flag"); got != want {
			t.Errorf("Bool(%s) = %v", raw, got)
		}
	}
	if c.Int("n", 0, 0, 100) != 42 || c.Int("missing", 7, 0, 100) != 7 {
		t.Error("Int")
	}
	if c.Float("f", 0, 0, 1) != 0.5 || c.Float("missing", 2, 0, 3) != 2 {
		t.Error("Float")
	}
	if v := c.OptionalFloat("opt", 0, 1); v == nil || *v != 0.25 {
		t.Error("OptionalFloat")
	}
	if c.OptionalFloat("missing", 0, 1) != nil {
		t.Error("OptionalFloat missing")
	}
	if c.BlockStatus("status") != 451 || c.BlockStatus("missing") != 0 {
		t.Error("BlockStatus")
	}
	if got := c.List("list"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("List = %v", got)
	}
	if c.List("missing") != nil {
		t.Error("List missing")
	}
	if got := c.Lines("lines"); !reflect.DeepEqual(got, []string{"one", "", "# two"}) {
		t.Errorf("Lines = %v", got)
	}
	if got := c.Roles("roles"); !reflect.DeepEqual(got, map[Role]bool{RoleSystem: true, RoleDeveloper: true, RoleUser: true}) {
		t.Errorf("Roles = %v", got)
	}
	if got := c.Roles("missing", RoleUser); !reflect.DeepEqual(got, map[Role]bool{RoleUser: true}) {
		t.Errorf("Roles default = %v", got)
	}
	if c.Raw("name") == nil || c.Raw("missing") != nil {
		t.Error("Raw")
	}
	if err := c.Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
	c = parse(t, `{"list": [" a ", "", "b"], "lines": ["x", "y"], "roles": [], "flag": false, "n": ""}`)
	if got := c.List("list"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("List array = %v", got)
	}
	if got := c.Lines("lines"); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("Lines array = %v", got)
	}
	if got := c.Roles("roles", RoleUser); len(got) != 0 {
		t.Errorf("Roles empty = %v", got)
	}
	if c.Bool("flag") || c.Int("n", 5, 0, 9) != 5 {
		t.Error("empty values keep defaults")
	}
}

func TestConfigErrors(t *testing.T) {
	tests := []struct {
		raw  string
		read func(c *Config)
		want string
	}{
		{`{"name": 5}`, func(c *Config) { c.String("name", "") }, "demo: name must be a string"},
		{`{"mode": "z"}`, func(c *Config) { c.Choice("mode", "a", "a", "b") }, `demo: mode must be one of a, b; got "z"`},
		{`{"flag": "maybe"}`, func(c *Config) { c.Bool("flag") }, "demo: flag must be true or false"},
		{`{"n": "abc"}`, func(c *Config) { c.Int("n", 0, 0, 9) }, `demo: n must be a number, got "abc"`},
		{`{"n": true}`, func(c *Config) { c.Int("n", 0, 0, 9) }, "demo: n must be a number, got true"},
		{`{"n": 1.5}`, func(c *Config) { c.Int("n", 0, 0, 9) }, "demo: n must be a whole number, got 1.5"},
		{`{"n": 10}`, func(c *Config) { c.Int("n", 0, 0, 9) }, "demo: n must be between 0 and 9, got 10"},
		{`{"f": 3}`, func(c *Config) { c.Float("f", 0, 0, 2) }, "demo: f must be between 0 and 2, got 3"},
		{`{"opt": -1}`, func(c *Config) { c.OptionalFloat("opt", 0, 1) }, "demo: opt must be between 0 and 1, got -1"},
		{`{"status": 302}`, func(c *Config) { c.BlockStatus("status") }, "demo: status must be an HTTP status between 400 and 599, got 302"},
		{`{"status": 600}`, func(c *Config) { c.BlockStatus("status") }, "demo: status must be an HTTP status between 400 and 599, got 600"},
		{`{"list": 5}`, func(c *Config) { c.List("list") }, "demo: list must be a list of strings"},
		{`{"lines": 5}`, func(c *Config) { c.Lines("lines") }, "demo: lines must be text or a list of strings"},
		{`{"roles": ["robot"]}`, func(c *Config) { c.Roles("roles") }, `demo: roles has unknown role "robot"`},
	}
	for _, tt := range tests {
		c := parse(t, tt.raw)
		tt.read(c)
		if err := c.Err(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.raw, err, tt.want)
		}
	}
	// The first problem wins and later reads keep their defaults.
	c := parse(t, `{"name": 5, "n": "x"}`)
	if c.String("name", "d") != "d" || c.Int("n", 3, 0, 9) != 3 {
		t.Error("defaults on error")
	}
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "name must be a string") {
		t.Errorf("first error = %v", err)
	}
}

func TestEnforcement(t *testing.T) {
	e := Enforcement{Action: ActionBlock, Message: "no", BlockStatus: 451}
	if d := e.Enforce("c", 1); d.Action != ActionBlock || d.Status != 451 || d.Code != "c" || d.Message != "no" || d.Detail != 1 {
		t.Errorf("block = %+v", d)
	}
	e.Action = ActionRespond
	if d := e.Enforce("c", nil); d.Action != ActionRespond || d.Code != "c" || d.Response.Text(0) != "no" {
		t.Errorf("respond = %+v", d)
	}
	e.RespondText = "sorry"
	if d := e.Reject("c", nil); d.Response.Text(0) != "sorry" {
		t.Errorf("respond text = %+v", d)
	}
	e.Action = ActionWarn
	if d := e.Enforce("c", 2); d.Action != ActionWarn || d.Code != "c" || d.Message != "no" || d.Detail != 2 {
		t.Errorf("warn = %+v", d)
	}
	if d := e.Reject("c", nil); d.Action != ActionBlock || d.Status != 451 {
		t.Errorf("reject under warn = %+v", d)
	}
	if f := BlockStatusField(); f.Key != "block_status" || f.Input != InputNumber || f.Help == "" {
		t.Errorf("field = %+v", f)
	}
}
