package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

const exportTestConfig = `
server:
  master_key: ${env:EXPORT_TEST_MASTER_KEY}
providers:
  openai:
    type: openai
    api_key: ${env:EXPORT_TEST_OPENAI_KEY}
    workload_identity:
      identity_token: wi-literal-do-not-leak
users:
  - path: /declared
    description: from config.yaml
budgets:
  enabled: true
  user_paths:
    - path: /team
      limits:
        - period: daily
          amount: 10
tagging:
  headers:
    - header: X-Team
`

// loadExportConfig loads exportTestConfig from a fresh directory, with a
// fresh SQLite database.
func loadExportConfig(t *testing.T, data string) *config.LoadResult {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("config", 0o755))
	require.NoError(t, os.WriteFile("config/config.yaml", []byte(data), 0o600))
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "export.db"))
	loaded, err := config.Load()
	require.NoError(t, err, data)
	return loaded
}

// isolateEnv unsets every environment variable but those the OS and test
// runner need, so a developer's OPENAI_API_KEY or GOMODEL_MASTER_KEY does
// not change what the test loads. They are restored when the test ends.
func isolateEnv(t *testing.T) {
	t.Helper()
	keep := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if name == "" || keep[strings.ToUpper(name)] {
			continue
		}
		t.Setenv(name, value)
		require.NoError(t, os.Unsetenv(name))
	}
}

func exportTestFactory() *providers.ProviderFactory {
	factory := providers.NewProviderFactory()
	factory.Add(openai.Registration)
	return factory
}

// seedDashboardObjects saves the objects a dashboard user would: some with
// literal secrets, one holding a reference, some shadowed by config.yaml,
// and a user description written to read the master key once reloaded.
func seedDashboardObjects(t *testing.T, db sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	creds, err := providers.NewSQLCredentialStore(ctx, db)
	require.NoError(t, err)
	for _, cred := range []providers.ManagedProviderCredential{
		{Name: "groq", Type: "groq", APIKeys: []string{"gsk-literal-do-not-leak", "${env:EXPORT_TEST_GROQ_SECOND}"}, Enabled: true},
		{Name: "old", Type: "openai", APIKeys: []string{"sk-old-do-not-leak"}},
		{Name: "openai", Type: "openai", APIKeys: []string{"sk-shadowed-do-not-leak"}, Enabled: true},
	} {
		require.NoError(t, creds.Upsert(ctx, cred))
	}
	servers, err := mcpgateway.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, servers.Upsert(ctx, mcpgateway.ManagedServer{
		Name: "linear", URL: "https://mcp.linear.app/mcp", Transport: "http", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer lin-literal-do-not-leak", "X-Org": "${env:EXPORT_TEST_LINEAR_ORG}"},
	}))
	userStore, err := users.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, userStore.Upsert(ctx, users.User{UserPath: "/declared", Description: "shadowed"}))
	require.NoError(t, userStore.Upsert(ctx, users.User{UserPath: "/tenant", Description: "${env:EXPORT_TEST_MASTER_KEY}"}))
	models, err := virtualmodels.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, models.Upsert(ctx, virtualmodels.VirtualModel{Source: "smart", Targets: []virtualmodels.Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "high", Enabled: true}))
	rules, err := tagging.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, rules.SaveRules(ctx, []tagging.Rule{{Header: "X-Cost-Center", Prefix: "cc-", Delimiter: ","}, {Header: "x-team"}}))
	budgets, err := budget.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, budgets.UpsertBudgets(ctx, []budget.Budget{
		{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 99, Source: budget.SourceManual},
		{Scope: budget.ScopeLabel, Subject: "mobile", PeriodSeconds: 7200, Amount: 3, CostBasis: budget.CostBasisBilled, Source: budget.SourceManual},
	}))
}

func TestExportConfig(t *testing.T) {
	isolateEnv(t)
	t.Setenv("EXPORT_TEST_MASTER_KEY", "mk-do-not-leak")
	t.Setenv("EXPORT_TEST_OPENAI_KEY", "sk-env-do-not-leak")
	t.Setenv("GOMODEL_ENCRYPTION_KEY", "export-test-key")
	loaded := loadExportConfig(t, exportTestConfig)
	withSQL(t, loaded, func(db sqlx.DB) { seedDashboardObjects(t, db) })
	ctx := context.Background()
	// Sealed at rest, as the dashboard stores them.
	_, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.NoError(t, err)

	out, err := ExportConfig(ctx, ConfigExportOptions{Config: loaded, Factory: exportTestFactory(), Now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	require.NoError(t, err)
	body := string(out)

	for _, secret := range []string{"mk-do-not-leak", "sk-env-do-not-leak", "gsk-literal", "sk-old", "sk-shadowed", "lin-literal", "wi-literal", "export-test-key"} {
		assert.NotContains(t, body, secret)
	}
	assert.True(t, strings.HasPrefix(body, "# GoModel configuration exported 2026-01-02T03:04:05Z"), body)

	var doc struct {
		*config.Config `yaml:",inline"`
		Providers      map[string]config.RawProviderConfig `yaml:"providers"`
	}
	doc.Config = &config.Config{}
	decoder := yaml.NewDecoder(strings.NewReader(body))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&doc), body)

	assert.Equal(t, "${env:EXPORT_TEST_MASTER_KEY}", doc.Server.MasterKey)
	assert.Equal(t, "${env:EXPORT_TEST_OPENAI_KEY}", doc.Providers["openai"].APIKey)
	assert.Equal(t, "${env:GOMODEL_PROVIDERS_GROQ_API_KEY}", doc.Providers["groq"].APIKey)
	assert.Equal(t, []string{"${env:EXPORT_TEST_GROQ_SECOND}"}, doc.Providers["groq"].APIKeys)
	assert.Equal(t, "${env:GOMODEL_PROVIDERS_OPENAI_WORKLOAD_IDENTITY_IDENTITY_TOKEN}", doc.Providers["openai"].WorkloadIdentity.IdentityToken)
	assert.NotContains(t, doc.Providers, "old")
	linear := doc.MCP.Servers["linear"]
	assert.Equal(t, "Bearer ${env:GOMODEL_MCP_SERVERS_LINEAR_HEADERS_AUTHORIZATION}", linear.Headers["Authorization"])
	assert.Equal(t, "${env:EXPORT_TEST_LINEAR_ORG}", linear.Headers["X-Org"])
	assert.Equal(t, []config.UserConfig{
		{Path: "/declared", Description: "from config.yaml"},
		{Path: "/tenant", Description: "$${env:EXPORT_TEST_MASTER_KEY}"},
	}, doc.Users)
	require.Len(t, doc.VirtualModels, 1)
	assert.Equal(t, "smart", doc.VirtualModels[0].Source)
	assert.Equal(t, "high", doc.VirtualModels[0].ReasoningEffort)
	assert.Equal(t, []config.TaggingHeaderConfig{{Header: "X-Team", Delimiter: ","}, {Header: "X-Cost-Center", Prefix: "cc-", Delimiter: ","}}, doc.Tagging.Headers)
	// The amount edited in the dashboard wins over config.yaml's, as at start.
	assert.Equal(t, []config.BudgetUserPathConfig{{Path: "/team", Limits: []config.BudgetLimitConfig{{Period: "daily", PeriodSeconds: budget.PeriodDailySeconds, Amount: 99, CostBasis: "provider"}}}}, doc.Budgets.UserPaths)
	assert.True(t, doc.Budgets.ProviderBilledCacheHits, "settings beside the budgets keep their value")
	assert.Equal(t, []config.BudgetLabelConfig{{Label: "mobile", Limits: []config.BudgetLimitConfig{{PeriodSeconds: 7200, Amount: 3, CostBasis: "billed"}}}}, doc.Budgets.Labels)

	// Loaded as config.yaml, the export runs the same gateway, and the
	// description reads back as text, not the master key.
	t.Setenv("GOMODEL_PROVIDERS_GROQ_API_KEY", "gsk-new")
	t.Setenv("GOMODEL_MCP_SERVERS_LINEAR_HEADERS_AUTHORIZATION", "lin-new")
	t.Setenv("EXPORT_TEST_GROQ_SECOND", "gsk-second")
	t.Setenv("EXPORT_TEST_LINEAR_ORG", "acme")
	reloaded := loadExportConfig(t, body)
	require.NoError(t, reloaded.ResolveSecrets(ctx))
	assert.Equal(t, "mk-do-not-leak", reloaded.Config.Server.MasterKey)
	assert.Equal(t, "${env:EXPORT_TEST_MASTER_KEY}", reloaded.Config.Users[1].Description)
	assert.Equal(t, "Bearer lin-new", reloaded.Config.MCP.Servers["linear"].Headers["Authorization"])
}

func TestExportRateLimitsPrefersDashboardEdits(t *testing.T) {
	ten, five, three := int64(10), int64(5), int64(3)
	declared := config.RateLimitsConfig{
		Enabled: true, FlushInterval: 7,
		UserPaths: []config.RateLimitUserPathConfig{
			{Path: "/team", Limits: []config.RateLimitRuleConfig{{Period: "minute", MaxRequests: &ten}}},
			{Path: "/other", Limits: []config.RateLimitRuleConfig{{Period: "minute", MaxRequests: &three}}},
		},
	}
	stored := []ratelimit.Rule{
		{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &five, Source: ratelimit.SourceManual},
		{Scope: ratelimit.ScopeUserPath, Subject: "/other", PeriodSeconds: 60, MaxRequests: &ten, Source: ratelimit.SourceConfig},
	}

	got, err := exportRateLimits(declared, stored)
	require.NoError(t, err)

	assert.Equal(t, 7, got.FlushInterval)
	require.Len(t, got.UserPaths, 2)
	limits := map[string]int64{}
	for _, entry := range got.UserPaths {
		require.Len(t, entry.Limits, 1)
		limits[entry.Path] = *entry.Limits[0].MaxRequests
	}
	assert.Equal(t, map[string]int64{"/team": 5, "/other": 3}, limits, "a dashboard edit wins; a stale config row does not")
}
