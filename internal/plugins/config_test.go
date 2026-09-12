package plugins

import (
	"errors"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

var errFake = errors.New("fake failure")

var testSchema = []pluginapi.Field{
	{Key: "mode", Input: pluginapi.InputSelect, Required: true, Default: "inject", Options: []pluginapi.Option{{Value: "inject"}, {Value: "override"}}},
	{Key: "content", Input: pluginapi.InputTextarea, Required: true},
	{Key: "max_tokens", Input: pluginapi.InputNumber, Default: 4096},
	{Key: "roles", Input: pluginapi.InputCheckboxes, Default: []string{"user"}, Options: []pluginapi.Option{{Value: "user"}, {Value: "system"}}},
	{Key: "api_key", Input: pluginapi.InputSecret},
	{Key: "entities", Input: pluginapi.InputList},
	{Key: "reversible", Input: pluginapi.InputBool},
	{Key: "window", Input: pluginapi.InputText, Scope: pluginapi.ScopeRoute},
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{
			name: "defaults applied and keys sorted",
			raw:  `{"content":" be safe "}`,
			want: `{"content":"be safe","max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name: "numeric string coerced",
			raw:  `{"content":"x","max_tokens":"12","mode":"override"}`,
			want: `{"content":"x","max_tokens":12,"mode":"override","roles":["user"]}`,
		},
		{
			name: "single checkbox string becomes list and dedupes",
			raw:  `{"content":"x","roles":"system"}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","roles":["system"]}`,
		},
		{
			name: "empty select falls back to default",
			raw:  `{"content":"x","mode":""}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name: "list accepts an array, trims, and dedupes",
			raw:  `{"content":"x","entities":[" PERSON ","EMAIL","PERSON",""]}`,
			want: `{"content":"x","entities":["PERSON","EMAIL"],"max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name: "list accepts comma and newline separated text",
			raw:  `{"content":"x","entities":"PERSON, EMAIL\nPHONE"}`,
			want: `{"content":"x","entities":["PERSON","EMAIL","PHONE"],"max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name: "empty list text is an empty list",
			raw:  `{"content":"x","entities":""}`,
			want: `{"content":"x","entities":[],"max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name:    "list rejects a number",
			raw:     `{"content":"x","entities":5}`,
			wantErr: "expected a list of strings",
		},
		{
			name: "bool accepts a boolean",
			raw:  `{"content":"x","reversible":true}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","reversible":true,"roles":["user"]}`,
		},
		{
			name: "bool accepts words and numbers",
			raw:  `{"content":"x","reversible":"Yes"}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","reversible":true,"roles":["user"]}`,
		},
		{
			name: "bool accepts 0",
			raw:  `{"content":"x","reversible":0}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","reversible":false,"roles":["user"]}`,
		},
		{
			name: "empty bool text is unset",
			raw:  `{"content":"x","reversible":""}`,
			want: `{"content":"x","max_tokens":4096,"mode":"inject","roles":["user"]}`,
		},
		{
			name:    "bool rejects other words",
			raw:     `{"content":"x","reversible":"maybe"}`,
			wantErr: "expected true or false",
		},
		{
			name:    "missing required",
			raw:     `{"mode":"inject"}`,
			wantErr: `"content" is required`,
		},
		{
			name:    "unknown key",
			raw:     `{"content":"x","bogus":1}`,
			wantErr: `unknown config key "bogus"`,
		},
		{
			name:    "route-scoped key is unknown in instance scope",
			raw:     `{"content":"x","window":"5m"}`,
			wantErr: `unknown config key "window"`,
		},
		{
			name:    "select option validated",
			raw:     `{"content":"x","mode":"nope"}`,
			wantErr: "not one of the allowed options",
		},
		{
			name:    "checkbox option validated",
			raw:     `{"content":"x","roles":["admin"]}`,
			wantErr: "not one of the allowed options",
		},
		{
			name:    "number type validated",
			raw:     `{"content":"x","max_tokens":"lots"}`,
			wantErr: "expected a number",
		},
		{
			name:    "not an object",
			raw:     `[1]`,
			wantErr: "JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateConfig(testSchema, json.RawMessage(tt.raw), pluginapi.ScopeInstance)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestValidateConfigEmptySchemaPassesThrough(t *testing.T) {
	got, err := ValidateConfig(nil, json.RawMessage(`{"b":1,"a":"x"}`), pluginapi.ScopeInstance)
	require.NoError(t, err)
	require.Equal(t, `{"a":"x","b":1}`, string(got), "got %s", got)

	got, err = ValidateConfig(nil, nil, pluginapi.ScopeInstance)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(got), "empty raw = %s, %v", got, err)
}

func TestSecrets(t *testing.T) {
	stored := json.RawMessage(`{"api_key":"s3cret","content":"x"}`)
	redacted := RedactSecrets(testSchema, stored)
	require.Equal(t, `{"api_key":"********","content":"x"}`, string(redacted), "RedactSecrets() = %s", redacted)

	merged := MergeSecrets(testSchema, redacted, stored)
	require.Equal(t, string(stored), string(merged))

	cleared := MergeSecrets(testSchema, json.RawMessage(`{"api_key":"","content":"x"}`), stored)
	require.Equal(t, `{"api_key":"","content":"x"}`, string(cleared), "MergeSecrets(cleared) = %s", cleared)
	got := RedactSecrets(testSchema, json.RawMessage(`{"content":"x"}`))
	require.Equal(t, `{"content":"x"}`, string(got), "RedactSecrets(no secret) = %s", got)
}

func TestSchemaDefaultsAndConfigHash(t *testing.T) {
	defaults := SchemaDefaults(testSchema)
	require.Equal(t, `{"api_key":"","content":"","entities":[],"max_tokens":4096,"mode":"inject","reversible":false,"roles":["user"]}`, string(defaults), "SchemaDefaults() = %s", defaults)

	a := ConfigHash(json.RawMessage(`{"b":1,"a":2}`))
	b := ConfigHash(json.RawMessage(`{"a":2, "b":1}`))
	require.Equal(t, b, a)
	require.Len(t, a, 16)
	require.NotEqual(t, a, ConfigHash(json.RawMessage(`{"a":3}`)))
}
