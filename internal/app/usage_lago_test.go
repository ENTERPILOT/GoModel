package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// With LAGO_ENABLED the gateway's usage logger bills each entry of a mapped
// customer in Lago, skips non-billable providers and unmapped callers, and
// sends what is still buffered when the gateway shuts down.
func TestNew_LagoExportBillsUsageAndFlushesOnShutdown(t *testing.T) {
	lago, capture := providertest.JSONServer(t, http.StatusOK, `{"events":[]}`)

	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "lago.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("LAGO_ENABLED", "true")
	t.Setenv("LAGO_API_URL", lago.URL+"/api/v1")
	t.Setenv("LAGO_API_KEY", "lago-test-key")
	t.Setenv("LAGO_SUBSCRIPTIONS", "/customers/acme=sub_acme")

	loaded, err := config.Load()
	require.NoError(t, err)
	ctx := context.Background()
	application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.NoError(t, err)
	application.providers.Registry.SetProviderBillable("customer-key", false)

	entry := func(id, userPath, providerName string) *usage.UsageEntry {
		return &usage.UsageEntry{
			ID:           id,
			Timestamp:    time.Unix(1_800_000_000, 0),
			Model:        "gpt-4o-mini",
			Provider:     "openai",
			ProviderName: providerName,
			UserPath:     userPath,
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		}
	}
	logger := application.UsageLogger()
	logger.Write(entry("billed", "/customers/acme/app", "openai"))
	logger.Write(entry("own-key", "/customers/acme/app", "customer-key"))
	logger.Write(entry("unmapped", "/customers/globex", "openai"))

	require.NoError(t, application.Shutdown(ctx))

	var ids []string
	for _, request := range capture.All() {
		assert.Equal(t, "/api/v1/events/batch", request.Path)
		assert.Equal(t, "Bearer lago-test-key", request.Header.Get("Authorization"))
		events, ok := request.JSON(t)["events"].([]any)
		require.True(t, ok)
		for _, event := range events {
			fields, ok := event.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "sub_acme", fields["external_subscription_id"])
			ids = append(ids, fields["transaction_id"].(string))
		}
	}
	assert.Equal(t, []string{"billed_tok_input", "billed_tok_output"}, ids)
}
