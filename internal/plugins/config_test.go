package plugins

import (
	"errors"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

var errFake = errors.New("fake failure")

var testSchema = []pluginapi.Field{
	{Key: "mode", Input: pluginapi.InputSelect, Required: true, Default: "inject", Options: []pluginapi.Option{{Value: "inject"}, {Value: "override"}}},
	{Key: "content", Input: pluginapi.InputTextarea, Required: true},
	{Key: "max_tokens", Input: pluginapi.InputNumber, Default: 4096},
	{Key: "roles", Input: pluginapi.InputCheckboxes, Default: []string{"user"}, Options: []pluginapi.Option{{Value: "user"}, {Value: "system"}}},
	{Key: "api_key", Input: pluginapi.InputSecret},
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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ValidateConfig() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateConfig() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("ValidateConfig() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestValidateConfigEmptySchemaPassesThrough(t *testing.T) {
	got, err := ValidateConfig(nil, json.RawMessage(`{"b":1,"a":"x"}`), pluginapi.ScopeInstance)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":"x","b":1}` {
		t.Fatalf("got %s", got)
	}
	got, err = ValidateConfig(nil, nil, pluginapi.ScopeInstance)
	if err != nil || string(got) != `{}` {
		t.Fatalf("empty raw = %s, %v", got, err)
	}
}

func TestSecrets(t *testing.T) {
	stored := json.RawMessage(`{"api_key":"s3cret","content":"x"}`)
	redacted := RedactSecrets(testSchema, stored)
	if string(redacted) != `{"api_key":"********","content":"x"}` {
		t.Fatalf("RedactSecrets() = %s", redacted)
	}
	merged := MergeSecrets(testSchema, redacted, stored)
	if string(merged) != string(stored) {
		t.Fatalf("MergeSecrets() = %s, want %s", merged, stored)
	}
	cleared := MergeSecrets(testSchema, json.RawMessage(`{"api_key":"","content":"x"}`), stored)
	if string(cleared) != `{"api_key":"","content":"x"}` {
		t.Fatalf("MergeSecrets(cleared) = %s", cleared)
	}
	if got := RedactSecrets(testSchema, json.RawMessage(`{"content":"x"}`)); string(got) != `{"content":"x"}` {
		t.Fatalf("RedactSecrets(no secret) = %s", got)
	}
}

func TestSchemaDefaultsAndConfigHash(t *testing.T) {
	defaults := SchemaDefaults(testSchema)
	if string(defaults) != `{"api_key":"","content":"","max_tokens":4096,"mode":"inject","roles":["user"]}` {
		t.Fatalf("SchemaDefaults() = %s", defaults)
	}
	a := ConfigHash(json.RawMessage(`{"b":1,"a":2}`))
	b := ConfigHash(json.RawMessage(`{"a":2, "b":1}`))
	if a != b || len(a) != 16 {
		t.Fatalf("ConfigHash not canonical: %q vs %q", a, b)
	}
	if ConfigHash(json.RawMessage(`{"a":3}`)) == a {
		t.Fatal("ConfigHash did not change with content")
	}
}
