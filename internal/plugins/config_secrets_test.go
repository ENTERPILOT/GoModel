package plugins

import (
	"errors"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

var secretSchema = []pluginapi.Field{
	{Key: "api_key", Input: pluginapi.InputSecret},
	{Key: "token", Input: pluginapi.InputSecret},
	{Key: "endpoint", Input: pluginapi.InputText},
}

func TestRedactSecretsKeepsReferences(t *testing.T) {
	raw := json.RawMessage(`{"api_key":"${vault:pii#key}","endpoint":"https://x","token":"literal"}`)
	redacted := RedactSecrets(secretSchema, raw)
	assert.JSONEq(t, `{"api_key":"${vault:pii#key}","endpoint":"https://x","token":"********"}`, string(redacted))
}

func TestMergeSecretsKeepsReferenceSentBack(t *testing.T) {
	stored := json.RawMessage(`{"api_key":"${vault:old}","token":"literal"}`)
	incoming := json.RawMessage(`{"api_key":"${vault:new}","token":"********"}`)
	assert.JSONEq(t, `{"api_key":"${vault:new}","token":"literal"}`, string(MergeSecrets(secretSchema, incoming, stored)))
}

func TestSecretValuesAndMapSecrets(t *testing.T) {
	raw := json.RawMessage(`{"api_key":"a","endpoint":"https://x","token":""}`)
	assert.Equal(t, map[string]string{"api_key": "a"}, SecretValues(secretSchema, raw))

	mapped, err := MapSecrets(secretSchema, raw, func(key, value string) (string, error) {
		return key + "=" + value, nil
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"api_key":"api_key=a","endpoint":"https://x","token":""}`, string(mapped))

	same, err := MapSecrets(secretSchema, raw, func(_, value string) (string, error) { return value, nil })
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(same), "an unchanged config is returned as is")

	_, err = MapSecrets(secretSchema, raw, func(string, string) (string, error) { return "", errors.New("boom") })
	require.EqualError(t, err, "boom")
}

func TestRedactSecretsMasksMixedValues(t *testing.T) {
	raw := json.RawMessage(`{"api_key":"sk-literal-${env:TAIL}"}`)
	assert.JSONEq(t, `{"api_key":"********"}`, string(RedactSecrets(secretSchema, raw)))
}
