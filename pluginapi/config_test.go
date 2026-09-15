package pluginapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSchema = []Field{
	{Key: "name"}, {Key: "mode"}, {Key: "flag"}, {Key: "n"}, {Key: "f"}, {Key: "opt"},
	{Key: "status"}, {Key: "list"}, {Key: "lines"}, {Key: "roles"},
}

func parse(t *testing.T, raw string) *Config {
	t.Helper()
	c, err := ParseConfig("demo", testSchema, json.RawMessage(raw))
	require.NoError(t, err, "ParseConfig(%s): %v", raw, err)

	return c
}

func TestParseConfigRejectsUnknownAndInvalid(t *testing.T) {
	for raw, want := range map[string]string{
		`{"bogus": 1}`: `demo: invalid config: unknown field "bogus"`,
		`{"name": `:    "demo: invalid config:",
		`[1]`:          "demo: invalid config:",
	} {
		_, err := ParseConfig("demo", testSchema, json.RawMessage(raw))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), want)
	}
	for _, raw := range []string{``, `  `, `null`, `{}`} {
		c, err := ParseConfig("demo", testSchema, json.RawMessage(raw))
		assert.NoError(t, err)
		assert.Equal(t, "d", c.String("name", "d"), "%q: %v", raw, err)
	}
}

func TestConfigReaders(t *testing.T) {
	c := parse(t, `{"name": "x", "mode": "b", "flag": "yes", "n": "42", "f": 0.5, "opt": "0.25", "status": 451, "list": "a, b\n c,,", "lines": "one\n\n# two", "roles": "system, USER"}`)
	assert.Equal(t, "x", c.String("name", ""))
	assert.Equal(t, "d", c.String("missing", "d"))
	assert.Equal(t, "b", c.Choice("mode", "a", "a", "b"))
	assert.Equal(t, "a", c.Choice("missing", "a", "a", "b"))
	assert.True(t, c.Bool("flag"))
	assert.False(t, c.Bool("missing"))

	for raw, want := range map[string]bool{`true`: true, `false`: false, `"on"`: true, `"off"`: false, `"1"`: true, `"0"`: false, `"YES"`: true, `"no"`: false, `""`: false} {
		got := parse(t, `{"flag": `+raw+`}`).Bool("flag")
		assert.Equal(t, want, got, "Bool(%s) = %v", raw, got)
	}
	assert.Equal(t, 42, c.Int("n", 0, 0, 100))
	assert.Equal(t, 7, c.Int("missing", 7, 0, 100))
	assert.Equal(t, 0.5, c.Float("f", 0, 0, 1))
	assert.Equal(t, float64(2), c.Float("missing", 2, 0, 3))
	v := c.OptionalFloat("opt", 0, 1)
	require.NotNil(t, v)
	assert.Equal(t, 0.25, *v)
	assert.Nil(t, c.OptionalFloat("missing", 0, 1))
	assert.Equal(t, 451, c.BlockStatus("status"))
	assert.Equal(t, 0, c.BlockStatus("missing"))
	got := c.List("list")
	assert.Equal(t, []string{"a", "b", "c"}, got)
	assert.Nil(t, c.List("missing"))
	got = c.Lines("lines")
	assert.Equal(t, []string{"one", "", "# two"}, got)

	if got := c.Roles("roles"); !reflect.DeepEqual(got, map[Role]bool{RoleSystem: true, RoleDeveloper: true, RoleUser: true}) {
		t.Errorf("Roles = %v", got)
	}
	if got := c.Roles("missing", RoleUser); !reflect.DeepEqual(got, map[Role]bool{RoleUser: true}) {
		t.Errorf("Roles default = %v", got)
	}
	assert.NotNil(t, c.Raw("name"))
	assert.Nil(t, c.Raw("missing"))
	err := c.Err()
	assert.NoError(t, err)

	c = parse(t, `{"list": [" a ", "", "b"], "lines": ["x", "y"], "roles": [], "flag": false, "n": ""}`)
	got = c.List("list")
	assert.Equal(t, []string{"a", "b"}, got)
	got = c.Lines("lines")
	assert.Equal(t, []string{"x", "y"}, got)

	if got := c.Roles("roles", RoleUser); len(got) != 0 {
		t.Errorf("Roles empty = %v", got)
	}
	assert.False(t, c.Bool("flag"))
	assert.Equal(t, 5, c.Int("n", 5, 0, 9))
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
		err := c.Err()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), tt.want)
	}
	// The first problem wins and later reads keep their defaults.
	c := parse(t, `{"name": 5, "n": "x"}`)
	assert.Equal(t, "d", c.String("name", "d"))
	assert.Equal(t, 3, c.Int("n", 3, 0, 9))
	err := c.Err()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name must be a string")
}

func TestEnforcement(t *testing.T) {
	e := Enforcement{Action: ActionBlock, Message: "no", BlockStatus: 451}
	d := e.Enforce("c", 1)
	assert.Equal(t, ActionBlock, d.Action)
	assert.Equal(t, 451, d.Status)
	assert.Equal(t, "c", d.Code)
	assert.Equal(t, "no", d.Message)
	assert.Equal(t, 1, d.Detail, "block = %+v", d)

	e.Action = ActionRespond
	d = e.Enforce("c", nil)
	assert.Equal(t, ActionRespond, d.Action)
	assert.Equal(t, "c", d.Code)
	assert.Equal(t, "no", d.Response.Text(0), "respond = %+v", d)

	e.RespondText = "sorry"
	d = e.Reject("c", nil)
	assert.Equal(t, "sorry", d.Response.Text(0), "respond text = %+v", d)

	e.Action = ActionWarn
	d = e.Enforce("c", 2)
	assert.Equal(t, ActionWarn, d.Action)
	assert.Equal(t, "c", d.Code)
	assert.Equal(t, "no", d.Message)
	assert.Equal(t, 2, d.Detail, "warn = %+v", d)
	d = e.Reject("c", nil)
	assert.Equal(t, ActionBlock, d.Action)
	assert.Equal(t, 451, d.Status, "reject under warn = %+v", d)
	f := BlockStatusField()
	assert.Equal(t, "block_status", f.Key)
	assert.Equal(t, InputNumber, f.Input)
	assert.NotEmpty(t, f.Help, "field = %+v", f)
}
