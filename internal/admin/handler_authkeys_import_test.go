package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

func TestImportAuthKey(t *testing.T) {
	const token = "sk-Zx9v2Qm7Lp4Tn8Wb1Kc6Hd"
	sum := sha256.Sum256([]byte(token))
	body := `{"name":"search-team","user_path":"/acme/search","labels":["team-search"],` +
		`"imported_from":"litellm","secret_hash":"` + hex.EncodeToString(sum[:]) + `","redacted_value":"sk-...k6Hd"}`

	service, err := authkeys.NewService(newAuthKeyTestStore())
	require.NoError(t, err)
	h := NewHandler(nil, nil, WithAuthKeys(service))

	c, rec := echotest.Post(t, "/admin/auth-keys/import", body)
	require.NoError(t, h.ImportAuthKey(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	view := echotest.Decode[map[string]any](t, rec)
	assert.Equal(t, "litellm", view["imported_from"])
	assert.Equal(t, "sk-...k6Hd", view["redacted_value"])
	assert.Equal(t, "/acme/search", view["user_path"])
	assert.NotContains(t, view, "value", "an import never returns a token")
	assert.NotContains(t, view, "secret_hash")

	got, err := service.Authenticate(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, view["id"], got.ID)

	c, rec = echotest.Post(t, "/admin/auth-keys/import", body)
	require.NoError(t, h.ImportAuthKey(c))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "auth_key_exists", errorCode(t, rec))
}

func TestImportAuthKeyAllowsTheModelsBehindVirtualModels(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("openai/gpt-4o-mini", "openai")
	vms := newVMService(t, catalog, newVMTestStore(
		redirectVM("smart", "openai/gpt-4o", true),
		redirectVM("off", "openai/gpt-4o-mini", false),
		virtualmodels.VirtualModel{Source: "dead-end", Targets: []virtualmodels.Target{{Model: "off"}}, Enabled: true},
	), true)
	authKeys, err := authkeys.NewService(newAuthKeyTestStore())
	require.NoError(t, err)
	h := NewHandler(nil, nil, WithAuthKeys(authKeys), WithVirtualModels(vms))

	tests := []struct {
		models string
		want   []string
	}{
		{`["smart", "openai/gpt-4o-mini"]`, []string{"openai/gpt-4o", "openai/gpt-4o-mini"}},
		// A virtual model whose only target is disabled keeps its name, which
		// matches nothing, instead of leaving the key unrestricted.
		{`["dead-end"]`, []string{"dead-end"}},
	}
	for i, tt := range tests {
		body := `{"name":"k","imported_from":"litellm","secret_hash":"` + hexOf(tt.models) + `","allowed_models":` + tt.models + `}`
		c, rec := echotest.Post(t, "/admin/auth-keys/import", body)
		require.NoError(t, h.ImportAuthKey(c))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, tt.want, echotest.Decode[authkeys.View](t, rec).AllowedModels, "case %d", i)
	}
}

func TestImportAuthKeyRejectsInvalidInput(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	for _, body := range []string{
		`{"name":"k","imported_from":"litellm","secret_hash":"sk-Zx9v2Qm7Lp4Tn8Wb1Kc6Hd"}`,
		`{"name":"k","secret_hash":"` + hexOf("x") + `"}`,
		`{"name":"k","imported_from":"litellm","secret_hash":"` + hexOf("x") + `","redacted_value":"sk-Zx9v2Qm7Lp4Tn8Wb1Kc6Hd"}`,
		`not json`,
	} {
		c, rec := echotest.Post(t, "/admin/auth-keys/import", body)
		require.NoError(t, h.ImportAuthKey(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestImportAuthKeyReturns503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Post(t, "/admin/auth-keys/import", `{"name":"k"}`)
	require.NoError(t, h.ImportAuthKey(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func hexOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
