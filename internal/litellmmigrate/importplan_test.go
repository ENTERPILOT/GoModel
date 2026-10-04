package litellmmigrate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const importTestConfig = `
model_list:
  - model_name: gpt-4o
    litellm_params: {model: openai/gpt-4o, api_key: os.environ/OPENAI_API_KEY}
  - model_name: smart
    litellm_params: {model: openai/gpt-4o-mini, api_key: os.environ/OPENAI_API_KEY}
  - model_name: smart
    litellm_params: {model: anthropic/claude-haiku-4-5, api_key: os.environ/ANTHROPIC_API_KEY}
router_settings:
  fallbacks: [{"smart": ["gpt-4o"]}]
`

var importTestNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func planFor(t *testing.T, db *Database) (*ImportPlan, *Result) {
	t.Helper()
	_, result := convertYAML(t, importTestConfig)
	return PlanImport(db, result, importTestNow), result
}

func keyByName(t *testing.T, plan *ImportPlan, name string) KeyImport {
	t.Helper()
	for _, key := range plan.Keys {
		if key.Name == name {
			return key
		}
	}
	require.Failf(t, "key not planned", "%s", name)
	return KeyImport{}
}

func policyAt(plan *ImportPlan, path string) []string {
	for _, policy := range plan.Policies {
		if policy.UserPath == path {
			return policy.AllowedModels
		}
	}
	return nil
}

func TestPlanImport_UserPaths(t *testing.T) {
	plan, _ := planFor(t, &Database{
		Organizations: []dbOrganization{{OrganizationID: "org-1", OrganizationAlias: new("Acme")}},
		Teams: []dbTeam{
			{TeamID: "team-1", TeamAlias: new("Search EU/West:1"), OrganizationID: new("org-1")},
			{TeamID: "team-2"},
		},
		Users: []dbUser{{UserID: "u-1", UserEmail: new("alice@example.com")}},
		Keys: []dbKey{
			{Token: "hash-team", KeyAlias: new("prod"), TeamID: new("team-1")},
			{Token: "hash-team-2", KeyAlias: new("prod"), TeamID: new("team-1")},
			{Token: "hash-no-alias-team", KeyName: new("sk-...abcd"), TeamID: new("team-2")},
			{Token: "hash-personal", KeyAlias: new("laptop"), UserID: new("u-1")},
			{Token: "hash-unknown-user", KeyAlias: new("ci"), UserID: new("u-gone")},
			{Token: "abcdef0123456789", KeyAlias: new(".."), KeyName: new("..")},
		},
	})

	paths := map[string]string{}
	for _, key := range plan.Keys {
		paths[key.SecretHash] = key.UserPath
	}
	assert.Equal(t, map[string]string{
		"hash-team":          "/Acme/Search EU-West-1/prod",
		"hash-team-2":        "/Acme/Search EU-West-1/prod-2",
		"hash-no-alias-team": "/team-2/sk-...abcd",
		"hash-personal":      "/users/alice@example.com/laptop",
		"hash-unknown-user":  "/users/u-gone/ci",
		"abcdef0123456789":   "/keys/abcdef01",
	}, paths)
}

func TestPlanImport_ModelAccess(t *testing.T) {
	plan, result := planFor(t, &Database{
		Organizations: []dbOrganization{{OrganizationID: "org-1", Models: []string{"gpt-4o", "smart"}}},
		Teams: []dbTeam{
			{TeamID: "t-open", TeamAlias: new("open"), Models: []string{allProxyModels, "gpt-4o"}},
			{TeamID: "t-gpt", TeamAlias: new("gpt"), OrganizationID: new("org-1"), Models: []string{"gpt-4o"}},
		},
		Users: []dbUser{
			{UserID: "alice", Models: []string{"smart"}},
			{UserID: "bob", Models: []string{noDefaultModels}},
		},
		Keys: []dbKey{
			{Token: "h1", KeyAlias: new("team-default"), TeamID: new("t-gpt"), Models: []string{allTeamModels}},
			{Token: "h2", KeyAlias: new("smart-only"), Models: []string{"smart", "smart"}},
			{Token: "h3", KeyAlias: new("alice"), UserID: new("alice")},
			{Token: "h4", KeyAlias: new("bob"), UserID: new("bob")},
			{Token: "h5", KeyAlias: new("ui-model"), Models: []string{"made-in-the-ui"}},
		},
	})

	smart := []string{"openai/gpt-4o-mini", "anthropic/claude-haiku-4-5", "openai/gpt-4o"}
	assert.Equal(t, []string{"openai/gpt-4o", "openai/gpt-4o-mini", "anthropic/claude-haiku-4-5"}, policyAt(plan, "/org-1"),
		"names resolve to the models behind them, including a fallback's load balancer")
	assert.Equal(t, []string{"openai/gpt-4o"}, policyAt(plan, "/org-1/gpt"))
	assert.Nil(t, policyAt(plan, "/open"), "all-proxy-models leaves the team unrestricted")
	assert.Equal(t, smart, policyAt(plan, "/users/alice"), "the user's list binds the user's personal keys")
	assert.Equal(t, []string{noDefaultModels}, policyAt(plan, "/users/bob"), "no-default-models keeps personal keys closed")

	assert.Nil(t, keyByName(t, plan, "team-default").AllowedModels, "all-team-models defers to the team policy")
	assert.Equal(t, smart, keyByName(t, plan, "smart-only").AllowedModels)
	assert.Nil(t, keyByName(t, plan, "alice").AllowedModels)
	assert.Equal(t, []string{"made-in-the-ui"}, keyByName(t, plan, "ui-model").AllowedModels, "an unknown name stays and matches nothing")
	assert.Contains(t, findings(result, SeverityWarning), "models made-in-the-ui: named by LiteLLM keys, teams, or users but not a model_name in the LiteLLM config; access to it stays closed until it exists in GoModel")
}

func TestPlanImport_BudgetsAndRateLimits(t *testing.T) {
	plan, result := planFor(t, &Database{
		Organizations: []dbOrganization{{OrganizationID: "org-1", BudgetID: new("b-org"), Spend: 12.5}},
		Budgets: []dbBudget{
			{BudgetID: "b-org", MaxBudget: new(1000.0), BudgetDuration: new("30d"), RPMLimit: new(int64(500))},
			{BudgetID: "b-tier", MaxBudget: new(9.0), BudgetDuration: new("1w"), TPMLimit: new(int64(7))},
		},
		Teams: []dbTeam{{TeamID: "t", TeamAlias: new("team"), MaxBudget: new(100.0), BudgetDuration: new("1mo")}},
		Keys: []dbKey{
			{Token: "h1", KeyAlias: new("busy"), TeamID: new("t"),
				MaxBudget: new(5.0), BudgetDuration: new("24h"), RPMLimit: new(int64(60)), TPMLimit: new(int64(1000)),
				TPDLimit: new(int64(50000)), MaxParallelRequests: new(int64(4))},
			{Token: "h2", KeyAlias: new("tiered"), BudgetID: new("b-tier"), RPMLimit: new(int64(3))},
			{Token: "h3", KeyAlias: new("hourly"), MaxBudget: new(1.0), BudgetDuration: new("1h")},
			{Token: "h4", KeyAlias: new("half-day"), MaxBudget: new(2.0), BudgetDuration: new("12h")},
			{Token: "h5", KeyAlias: new("quarter"), MaxBudget: new(3.0), BudgetDuration: new("3mo")},
			{Token: "h6", KeyAlias: new("lifetime"), MaxBudget: new(4.0)},
			{Token: "h7", KeyAlias: new("odd"), MaxBudget: new(4.0), BudgetDuration: new("soon")},
		},
	})

	assert.ElementsMatch(t, []BudgetImport{
		{UserPath: "/org-1", Period: "monthly", Amount: 1000, Spend: 12.5},
		{UserPath: "/team", Period: "monthly", Amount: 100},
		{UserPath: "/team/busy", Period: "daily", Amount: 5},
		{UserPath: "/keys/tiered", Period: "weekly", Amount: 9},
		{UserPath: "/keys/hourly", Period: "hourly", Amount: 1},
		{UserPath: "/keys/half-day", PeriodSeconds: 12 * 3600, Amount: 2},
		{UserPath: "/keys/quarter", PeriodSeconds: 90 * 86400, Amount: 3},
	}, plan.Budgets)
	assert.ElementsMatch(t, []RateLimitImport{
		{UserPath: "/org-1", Period: "minute", MaxRequests: new(int64(500))},
		{UserPath: "/team/busy", Period: "minute", MaxRequests: new(int64(60)), MaxTokens: new(int64(1000))},
		{UserPath: "/team/busy", Period: "day", MaxTokens: new(int64(50000))},
		{UserPath: "/team/busy", Period: "concurrent", MaxRequests: new(int64(4))},
		{UserPath: "/keys/tiered", Period: "minute", MaxRequests: new(int64(3)), MaxTokens: new(int64(7))},
	}, plan.RateLimits, "a key's own limits win over its budget row's")

	skipped := findings(result, SeveritySkipped)
	assert.Contains(t, skipped, "key lifetime max_budget: $4 with no budget_duration never resets; GoModel budgets reset every period. Add one on /keys/lifetime if you need it")
	assert.Contains(t, skipped, `key odd budget_duration: "soon" is not a duration GoModel understands; the $4 budget was not migrated`)
	assert.Contains(t, findings(result, SeverityInfo), "organization org-1 budget_duration: 30d became a monthly budget, which resets on calendar months")
}

func TestPlanImport_Keys(t *testing.T) {
	expires := dbTime{importTestNow.Add(48 * time.Hour)}
	expired := dbTime{importTestNow.Add(-time.Minute)}
	plan, result := planFor(t, &Database{
		Teams: []dbTeam{
			{TeamID: "t", TeamAlias: new("team"), Metadata: map[string]any{"tags": []any{"search", "eu"}}},
			{TeamID: "t-blocked", TeamAlias: new("frozen"), Blocked: new(true)},
		},
		Keys: []dbKey{
			{Token: "h1", KeyAlias: new("prod"), KeyName: new("sk-...abcd"), TeamID: new("t"), Expires: &expires,
				Metadata: map[string]any{"tags": []any{"prod", "search", 7}}},
			{Token: "h2", KeyAlias: new("blocked"), Blocked: new(true)},
			{Token: "h3", KeyAlias: new("expired"), Expires: &expired},
			{Token: "h4", KeyAlias: new("frozen-key"), TeamID: new("t-blocked")},
			{Token: "h5", KeyName: new("my custom key name")},
		},
	})

	require.Len(t, plan.Keys, 2)
	assert.Equal(t, 3, plan.SkippedKeys)
	prod := keyByName(t, plan, "prod")
	assert.Equal(t, KeyImport{
		Name: "prod", ImportedFrom: "litellm", SecretHash: "h1", RedactedValue: "sk-...abcd",
		UserPath: "/team/prod", Labels: []string{"prod", "search", "eu"}, ExpiresAt: &expires.Time,
	}, prod)
	assert.Empty(t, keyByName(t, plan, "my custom key name").RedactedValue, "only LiteLLM's abbreviated form is shown")

	skipped := findings(result, SeveritySkipped)
	assert.Contains(t, skipped, "key blocked: blocked in LiteLLM")
	assert.Contains(t, skipped, "key expired: expired in LiteLLM")
	assert.Contains(t, skipped, "team frozen: blocked in LiteLLM; its keys are not imported")
}

func TestPlanImport_ReportSection(t *testing.T) {
	_, result := planFor(t, &Database{
		Teams: []dbTeam{{TeamID: "t", TeamAlias: new("team"), Models: []string{"gpt-4o"}, Spend: 3.2,
			MaxBudget: new(10.0), BudgetDuration: new("12h"), MaxParallelRequests: new(int64(2))}},
		Keys: []dbKey{{Token: "h", KeyAlias: new("k"), TeamID: new("t")}},
	})
	report := result.Report.Markdown()
	assert.Contains(t, report, "1 keys to import (0 skipped), 1 model policies, 1 budgets, 1 rate limits.")
	assert.Contains(t, report, "| `/team` | `openai/gpt-4o` | $10 every 12h (LiteLLM spent $3.20) | 2 concurrent requests |")
}

func TestParseBudgetDuration(t *testing.T) {
	tests := []struct {
		in      string
		seconds int64
		months  int
		ok      bool
	}{
		{"30s", 30, 0, true},
		{"15m", 900, 0, true},
		{" 1H ", 3600, 0, true},
		{"7d", 7 * 86400, 0, true},
		{"2w", 14 * 86400, 0, true},
		{"1mo", 0, 1, true},
		{"0d", 0, 0, false},
		{"d", 0, 0, false},
		{"1y", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		seconds, months, ok := parseBudgetDuration(tt.in)
		assert.Equal(t, tt.ok, ok, tt.in)
		assert.Equal(t, tt.seconds, seconds, tt.in)
		assert.Equal(t, tt.months, months, tt.in)
	}
}
