package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/tagging"
)

// exportedConfig is the layout config.yaml is read into, for decoding an
// export strictly.
type exportedConfig struct {
	*config.Config `yaml:",inline"`
	Providers      map[string]config.RawProviderConfig `yaml:"providers"`
}

func decodeExport(t *testing.T, body string) exportedConfig {
	t.Helper()
	doc := exportedConfig{Config: &config.Config{}}
	decoder := yaml.NewDecoder(strings.NewReader(body))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&doc), body)
	return doc
}

func TestExportConfigUnavailableWithoutSource(t *testing.T) {
	c, rec := echotest.Get(t, "/admin/config/export")
	require.NoError(t, NewHandler(nil, nil).ExportConfig(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestExportConfigMergesDashboardRows(t *testing.T) {
	t.Setenv("GOMODEL_EXPORT_TEST_KEY", "sk-env-openai")
	ctx := context.Background()

	cfg := &config.Config{
		Server:  config.ServerConfig{Port: "8080", MasterKey: "literal-master"},
		Budgets: config.BudgetsConfig{Enabled: true},
		Tagging: config.TaggingConfig{Headers: []config.TaggingHeaderConfig{{Header: "X-Team"}}},
	}
	declared := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "${env:GOMODEL_EXPORT_TEST_KEY}"},
	}

	credentials := newProviderCredentialsAdminFake()
	credentials.rows["groq"] = providers.ManagedProviderCredential{Name: "groq", Type: "groq", APIKeys: []string{"gsk-literal", "${vault:groq2}"}, Enabled: true}
	credentials.rows["old"] = providers.ManagedProviderCredential{Name: "old", Type: "openai", APIKeys: []string{"sk-old"}}
	credentials.rows["openai"] = providers.ManagedProviderCredential{Name: "openai", Type: "openai", APIKeys: []string{"sk-shadowed"}, Enabled: true}
	credentials.addManaged("openai")

	mcp := newMCPAdminFake()
	mcp.addStored(mcpgateway.ManagedServer{
		Name: "linear", URL: "https://mcp.linear.app/mcp", Transport: "http", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer lin-literal", "X-Org": "${env:LINEAR_ORG}"},
	}, mcpgateway.StatusConnected)

	taggingService := tagging.NewService(tagging.ConfigRules(cfg.Tagging.Headers), &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Cost-Center", Prefix: "cc-"}}})
	require.NoError(t, taggingService.Refresh(ctx))

	budgets, err := budget.NewService(ctx, &adminBudgetStore{budgets: []budget.Budget{
		{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10, Source: budget.SourceConfig},
		{Scope: budget.ScopeLabel, Subject: "mobile", PeriodSeconds: 7200, Amount: 3, Source: budget.SourceManual},
	}})
	require.NoError(t, err)

	rateLimits, err := ratelimit.NewService(ctx, &adminRateLimitStore{rules: []ratelimit.Rule{
		{Scope: ratelimit.ScopeProvider, Subject: "openai", SubjectDisplay: "OpenAI", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: new(int64(500))},
		{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: new(int64(4))},
	}})
	require.NoError(t, err)

	h := NewHandler(nil, nil,
		WithProviderCredentials(credentials),
		WithMCPServers(mcp),
		WithTagging(taggingService),
		WithBudgets(budgets),
		WithRateLimits(rateLimits),
		WithConfigExport(ConfigExportSource{Config: cfg, Providers: declared, Secrets: config.NewSecrets()}),
	)

	c, rec := echotest.Get(t, "/admin/config/export")
	require.NoError(t, h.ExportConfig(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `attachment; filename="gomodel-config.yaml"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()

	for _, secret := range []string{"literal-master", "sk-env-openai", "gsk-literal", "sk-old", "sk-shadowed", "lin-literal"} {
		assert.NotContains(t, body, secret)
	}
	assert.Contains(t, body, "Not exported: managed API keys; workflows; model pricing overrides; runtime settings; disabled providers (old).")

	doc := decodeExport(t, body)
	assert.Equal(t, "${env:GOMODEL_MASTER_KEY}", doc.Server.MasterKey)
	assert.Equal(t, "${env:GOMODEL_EXPORT_TEST_KEY}", doc.Providers["openai"].APIKey)
	assert.Equal(t, "${env:GOMODEL_PROVIDERS_GROQ_API_KEY}", doc.Providers["groq"].APIKey)
	assert.Equal(t, []string{"${vault:groq2}"}, doc.Providers["groq"].APIKeys)
	assert.NotContains(t, doc.Providers, "old")

	linear := doc.MCP.Servers["linear"]
	assert.Equal(t, "https://mcp.linear.app/mcp", linear.URL)
	assert.Equal(t, "Bearer ${env:GOMODEL_MCP_SERVERS_LINEAR_HEADERS_AUTHORIZATION}", linear.Headers["Authorization"])
	assert.Equal(t, "${env:LINEAR_ORG}", linear.Headers["X-Org"])

	assert.Equal(t, []config.TaggingHeaderConfig{{Header: "X-Team"}, {Header: "X-Cost-Center", Prefix: "cc-", Delimiter: ","}}, doc.Tagging.Headers)

	require.Len(t, doc.Budgets.UserPaths, 1)
	assert.Equal(t, "/team", doc.Budgets.UserPaths[0].Path)
	assert.Equal(t, []config.BudgetLimitConfig{{Period: "daily", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10}}, doc.Budgets.UserPaths[0].Limits)
	require.Len(t, doc.Budgets.Labels, 1)
	assert.Equal(t, []config.BudgetLimitConfig{{PeriodSeconds: 7200, Amount: 3}}, doc.Budgets.Labels[0].Limits)

	require.Len(t, doc.RateLimits.Providers, 1)
	assert.Equal(t, "OpenAI", doc.RateLimits.Providers[0].Name)
	assert.Equal(t, "minute", doc.RateLimits.Providers[0].Limits[0].Period)
	require.Len(t, doc.RateLimits.UserPaths, 1)
	assert.Equal(t, "concurrent", doc.RateLimits.UserPaths[0].Limits[0].Period)
}

func TestExportConfigLeavesLoadedConfigUnchanged(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Tagging: config.TaggingConfig{Headers: []config.TaggingHeaderConfig{{Header: "X-Team"}}}}
	taggingService := tagging.NewService(tagging.ConfigRules(cfg.Tagging.Headers), &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Cost-Center"}}})
	require.NoError(t, taggingService.Refresh(ctx))
	h := NewHandler(nil, nil, WithTagging(taggingService), WithConfigExport(ConfigExportSource{Config: cfg, Secrets: config.NewSecrets()}))

	c, rec := echotest.Get(t, "/admin/config/export")
	require.NoError(t, h.ExportConfig(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []config.TaggingHeaderConfig{{Header: "X-Team"}}, cfg.Tagging.Headers)
}
