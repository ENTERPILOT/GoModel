package guardrails

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoConfigFromRaw_NormalizesEmptyAndNullToEmptyDocument(t *testing.T) {
	t.Parallel()

	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(""),
		json.RawMessage("   "),
		json.RawMessage("null"),
	} {
		doc, err := mongoConfigFromRaw(raw)
		require.NoError(t, err)
		require.NotNil(t, doc)
		require.Empty(t, doc)
	}
}

func TestDefinitionFromMongo_NormalizesNilConfigToEmptyObject(t *testing.T) {
	t.Parallel()

	definition, err := definitionFromMongo(mongoDefinitionDocument{
		Name:   "policy",
		Type:   "system_prompt",
		Config: nil,
	})
	require.NoError(t, err)
	got := string(definition.Config)
	require.Equal(t, "{}", got)

	definition, err = definitionFromMongo(mongoDefinitionDocument{
		Name:   "policy",
		Type:   "system_prompt",
		Config: bson.M{},
	})
	require.NoError(t, err)
	got = string(definition.Config)
	require.Equal(t, "{}", got)
}
