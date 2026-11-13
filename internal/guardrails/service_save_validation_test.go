package guardrails

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedHeaderEditDefinition names a credential header, which header_edit
// skips when it loads a stored instance and rejects when one is saved.
func refusedHeaderEditDefinition(name string) Definition {
	return Definition{Name: name, Type: "header_edit", Config: json.RawMessage(`{"request_set":"Authorization: Bearer sk-secret"}`)}
}

func TestServiceUpsertRunsSaveValidation(t *testing.T) {
	store := newTestStore()
	service := newService(t, store, nil)

	err := service.Upsert(context.Background(), refusedHeaderEditDefinition("auth"))
	require.ErrorContains(t, err, `invalid header_edit config: header_edit: request_set line 1: header "Authorization" carries credentials and cannot be edited`)
	assert.True(t, IsValidationError(err))
	assert.NotContains(t, err.Error(), "sk-secret")
	assert.Equal(t, 0, service.Len())
	assert.Empty(t, store.definitions, "a rejected definition is not stored")

	// A config without the refused line saves.
	require.NoError(t, service.Upsert(context.Background(), Definition{Name: "auth", Type: "header_edit", Config: json.RawMessage(`{"request_set":"X-Team: platform"}`)}))
	assert.Equal(t, 1, service.Len())
}

// Save validation is for what an operator saves now. A definition that is
// already stored, or seeded from the configuration, keeps loading, and does
// not stand in the way of saving another guardrail.
func TestServiceSaveValidationDoesNotApplyToStoredOrSeededDefinitions(t *testing.T) {
	t.Run("stored", func(t *testing.T) {
		service := newService(t, newTestStore(refusedHeaderEditDefinition("auth")), nil)
		require.Equal(t, 1, service.Len())

		require.NoError(t, service.Upsert(context.Background(), systemPromptDefinition("other", "hello")))
		assert.Equal(t, 2, service.Len())
	})

	t.Run("seeded", func(t *testing.T) {
		service := newService(t, newTestStore(), nil)
		require.NoError(t, service.UpsertDefinitions(context.Background(), []Definition{refusedHeaderEditDefinition("auth")}))
		assert.Equal(t, 1, service.Len())
	})

	t.Run("saving the stored definition again is rejected", func(t *testing.T) {
		service := newService(t, newTestStore(refusedHeaderEditDefinition("auth")), nil)

		err := service.Upsert(context.Background(), refusedHeaderEditDefinition("auth"))
		require.Error(t, err)
		assert.True(t, IsValidationError(err))
		assert.Equal(t, 1, service.Len(), "the stored definition keeps serving")
	})
}
