package authkeys

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const liteLLMToken = "sk-Zx9v2Qm7Lp4Tn8Wb1Kc6Hd"

func importLiteLLMKey(t *testing.T, service *Service, input ImportInput) *View {
	t.Helper()
	if input.Name == "" {
		input.Name = "search-team"
	}
	if input.ImportedFrom == "" {
		input.ImportedFrom = ImportedFromLiteLLM
	}
	if input.SecretHash == "" {
		input.SecretHash = hashSecret(liteLLMToken)
	}
	view, err := service.Import(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, view)
	return view
}

func TestServiceImportAuthenticatesTheOldToken(t *testing.T) {
	store := newTestStore()
	service, err := NewService(store)
	require.NoError(t, err)

	view := importLiteLLMKey(t, service, ImportInput{
		Name:          "search-team",
		UserPath:      "/acme/search",
		Labels:        []string{"team-search"},
		AllowedModels: []string{"gpt-4o"},
		RedactedValue: "sk-...k6Hd",
	})
	assert.Equal(t, ImportedFromLiteLLM, view.ImportedFrom)
	assert.Equal(t, "sk-...k6Hd", view.RedactedValue)
	assert.True(t, view.Active)

	// The key survives a reload from storage, as after a restart.
	require.NoError(t, service.Refresh(context.Background()))

	got, err := service.Authenticate(context.Background(), liteLLMToken)
	require.NoError(t, err)
	assert.Equal(t, view.ID, got.ID)
	assert.Equal(t, "/acme/search", got.UserPath)
	assert.Equal(t, []string{"team-search"}, got.Labels)
	assert.Equal(t, []string{"gpt-4o"}, got.AllowedModels)
}

func TestServiceAuthenticateMatchesOnlyTheTokensOwnFormat(t *testing.T) {
	now := time.Now().UTC()
	// A GoModel key whose secret hash equals the SHA-256 of a LiteLLM-style
	// token, and an imported key whose hash equals the SHA-256 of a GoModel
	// secret: neither may be reached with the other format's token.
	native := AuthKey{ID: "native", Name: "native", SecretHash: hashSecret("sk-native"), Enabled: true, CreatedAt: now, UpdatedAt: now}
	imported := AuthKey{ID: "imported", Name: "imported", SecretHash: hashSecret("sk-imported"), ImportedFrom: ImportedFromLiteLLM, Enabled: true, CreatedAt: now, UpdatedAt: now}
	service, err := NewService(newTestStore(native, imported))
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	for _, token := range []string{"sk-native", TokenPrefix + "sk-imported", "sk-", "Zx9v2Qm7Lp4Tn8Wb1Kc6Hd", ""} {
		_, err := service.Authenticate(context.Background(), token)
		require.ErrorIs(t, err, ErrInvalidToken, "token %q", token)
	}

	got, err := service.Authenticate(context.Background(), TokenPrefix+"sk-native")
	require.NoError(t, err)
	assert.Equal(t, "native", got.ID)
	got, err = service.Authenticate(context.Background(), " sk-imported ")
	require.NoError(t, err)
	assert.Equal(t, "imported", got.ID)
}

func TestServiceImportedKeyCanBeDeactivated(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)
	view := importLiteLLMKey(t, service, ImportInput{})

	require.NoError(t, service.Deactivate(context.Background(), view.ID))

	_, err = service.Authenticate(context.Background(), liteLLMToken)
	require.ErrorIs(t, err, ErrInactive)
}

func TestServiceImportRejectsADuplicateHash(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)
	importLiteLLMKey(t, service, ImportInput{})

	_, err = service.Import(context.Background(), ImportInput{
		Name:         "again",
		ImportedFrom: ImportedFromLiteLLM,
		SecretHash:   strings.ToUpper(hashSecret(liteLLMToken)),
	})
	require.ErrorIs(t, err, ErrSecretHashExists)
}

func TestServiceImportReportsAHashAnotherInstanceStored(t *testing.T) {
	now := time.Now().UTC()
	store := newTestStore()
	service, err := NewService(store)
	require.NoError(t, err)
	// Another replica imported the key after this one last refreshed.
	store.keys["elsewhere"] = AuthKey{ID: "elsewhere", Name: "elsewhere", SecretHash: hashSecret(liteLLMToken), ImportedFrom: ImportedFromLiteLLM, Enabled: true, CreatedAt: now, UpdatedAt: now}

	_, err = service.Import(context.Background(), ImportInput{
		Name:         "again",
		ImportedFrom: ImportedFromLiteLLM,
		SecretHash:   hashSecret(liteLLMToken),
	})
	require.ErrorIs(t, err, ErrSecretHashExists)
}

func TestServiceImportNormalizesInput(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	view := importLiteLLMKey(t, service, ImportInput{
		SecretHash: " " + strings.ToUpper(hashSecret(liteLLMToken)) + " ",
	})
	assert.Equal(t, "sk-...", view.RedactedValue)

	_, err = service.Authenticate(context.Background(), liteLLMToken)
	require.NoError(t, err)
}

func TestServiceImportValidatesInput(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	valid := ImportInput{
		Name:         "search-team",
		ImportedFrom: ImportedFromLiteLLM,
		SecretHash:   hashSecret(liteLLMToken),
	}
	tests := []struct {
		name   string
		mutate func(*ImportInput)
		want   string
	}{
		{"missing name", func(in *ImportInput) { in.Name = " " }, "name is required"},
		{"unknown source", func(in *ImportInput) { in.ImportedFrom = "bifrost" }, "imported_from"},
		{"missing source", func(in *ImportInput) { in.ImportedFrom = "" }, "imported_from"},
		{"short hash", func(in *ImportInput) { in.SecretHash = "abc123" }, "secret_hash"},
		{"non-hex hash", func(in *ImportInput) { in.SecretHash = strings.Repeat("z", 64) }, "secret_hash"},
		{"token instead of hash", func(in *ImportInput) { in.SecretHash = liteLLMToken }, "secret_hash"},
		{"token as redacted value", func(in *ImportInput) { in.RedactedValue = liteLLMToken }, "redacted_value"},
		{"invalid user path", func(in *ImportInput) { in.UserPath = "/a/../b" }, "user_path"},
		{"expired", func(in *ImportInput) { in.ExpiresAt = &past }, "expires_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(newTestStore())
			require.NoError(t, err)
			input := valid
			tt.mutate(&input)

			_, err = service.Import(context.Background(), input)
			require.Error(t, err)
			assert.True(t, IsValidationError(err), "error %v is not a validation error", err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, service.Total())
		})
	}
}
