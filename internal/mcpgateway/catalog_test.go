package mcpgateway

import (
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespacedName(t *testing.T) {
	t.Parallel()
	full := NamespacedName("github", "create_issue")
	require.Equal(t, "github_create_issue", full)
}

func TestBareToolAliases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		owners map[string]string
		want   map[string]string
	}{
		{
			name:   "unique bare names alias",
			owners: map[string]string{"alpha_echo": "alpha", "beta_search": "beta"},
			want:   map[string]string{"echo": "alpha_echo", "search": "beta_search"},
		},
		{
			name:   "ambiguous bare name gets no alias",
			owners: map[string]string{"alpha_echo": "alpha", "beta_echo": "beta"},
			want:   map[string]string{},
		},
		{
			name: "bare name colliding with a namespaced name gets no alias",
			// beta's tool is literally named "alpha_echo"; the namespaced
			// registration must keep winning for that exact name.
			owners: map[string]string{"alpha_echo": "alpha", "beta_alpha_echo": "beta"},
			want:   map[string]string{"echo": "alpha_echo"},
		},
		{
			name: "server names containing the separator stay unambiguous",
			owners: map[string]string{
				"git_hub_sync": "git_hub",
				"git_clone":    "git",
			},
			want: map[string]string{"sync": "git_hub_sync", "clone": "git_clone"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := bareToolAliases(tt.owners)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFilterTools(t *testing.T) {
	t.Parallel()
	tools := []*mcp.Tool{
		{Name: "write"},
		{Name: "read"},
		{Name: "admin"},
		nil,
		{Name: ""},
	}

	tests := []struct {
		name       string
		allowed    []string
		disallowed []string
		want       []string
	}{
		{name: "no filters keeps all sorted", want: []string{"admin", "read", "write"}},
		{name: "allowlist restricts", allowed: []string{"read", "write"}, want: []string{"read", "write"}},
		{name: "denylist applies after allowlist", allowed: []string{"read", "write"}, disallowed: []string{"write"}, want: []string{"read"}},
		{name: "denylist alone", disallowed: []string{"admin"}, want: []string{"read", "write"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filtered := filterTools(discoverTools(tools), tt.allowed, tt.disallowed)
			names := make([]string, 0, len(filtered))
			for _, tool := range filtered {
				names = append(names, tool.Name)
			}
			require.True(t, slices.Equal(names, tt.want), "filterTools() = %v, want %v", names, tt.want)
		})
	}
}

func TestDiscoverToolsNormalizesSchemasForDownstream(t *testing.T) {
	t.Parallel()
	validInput := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}
	tools := []*mcp.Tool{
		{Name: "missing"},
		{Name: "invalid", InputSchema: map[string]any{"type": "string"}, OutputSchema: []string{"not", "a", "schema"}},
		{Name: "valid", InputSchema: validInput, OutputSchema: map[string]any{"type": "object"}},
	}

	discovered := discoverTools(tools)
	byName := make(map[string]*mcp.Tool, len(discovered))
	for _, tool := range discovered {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"missing", "invalid", "valid"} {
		require.True(t, isObjectSchema(byName[name].InputSchema), "%s input schema = %#v, want object schema", name, byName[name].InputSchema)
	}
	require.Nil(t, byName["invalid"].OutputSchema)
	require.Equal(t, validInput, byName["valid"].InputSchema)
	require.True(t, isObjectSchema(byName["valid"].OutputSchema), "valid output schema = %#v, want preserved object", byName["valid"].OutputSchema)
}

func TestCatalogWithToolFiltersKeepsDiscoveredTools(t *testing.T) {
	t.Parallel()
	base := &catalog{discovered: discoverTools([]*mcp.Tool{{Name: "write"}, {Name: "read"}})}

	filtered := base.withToolFilters(nil, []string{"write"})
	require.Len(t, filtered.tools, 1)
	assert.Equal(t, "read", filtered.tools[0].Name)
	assert.Equal(t, 1, filtered.excludedToolCount())
	assert.Len(t, filtered.discovered, 2)
	assert.Empty(t, base.tools, "withToolFilters must not mutate the receiver")

	var missing *catalog
	assert.Nil(t, missing.withToolFilters(nil, []string{"write"}))
}

func TestCatalogToolRelaysExplicitSafetyHints(t *testing.T) {
	t.Parallel()
	destructive := true
	tests := []struct {
		name            string
		annotations     *mcp.ToolAnnotations
		wantReadOnly    bool
		wantDestructive bool
	}{
		{name: "unannotated tool has no hints"},
		{name: "read-only hint", annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, wantReadOnly: true},
		{name: "explicit destructive hint", annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}, wantDestructive: true},
		{name: "read-only wins over destructive", annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive}, wantReadOnly: true},
		{name: "implicit destructive default is not shown", annotations: &mcp.ToolAnnotations{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			feature := catalogTool(&mcp.Tool{Name: "tool", Annotations: tt.annotations})
			assert.Equal(t, tt.wantReadOnly, feature.ReadOnly)
			assert.Equal(t, tt.wantDestructive, feature.Destructive)
		})
	}
}

func TestServerSpecVisibleTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		userPath   string
		allowed    []string
		disallowed []string
		want       bool
	}{
		{name: "no scope admits everyone", userPath: "/x", want: true},
		{name: "excluded subtree is hidden", userPath: "/contractors/acme", disallowed: []string{"/contractors"}, want: false},
		{name: "sibling of excluded subtree stays visible", userPath: "/staff", disallowed: []string{"/contractors"}, want: true},
		{name: "exclusion wins inside an allowed subtree", userPath: "/eng/contractors", allowed: []string{"/eng"}, disallowed: []string{"/eng/contractors"}, want: false},
		{name: "rest of allowed subtree stays visible", userPath: "/eng/platform", allowed: []string{"/eng"}, disallowed: []string{"/eng/contractors"}, want: true},
		{name: "caller without user path is not in an excluded subtree", userPath: "", disallowed: []string{"/contractors"}, want: true},
		{name: "root exclusion hides from every caller", userPath: "", disallowed: []string{"/"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := ServerSpec{
				UserPaths:           normalizeUserPaths(tt.allowed),
				DisallowedUserPaths: normalizeUserPaths(tt.disallowed),
			}
			assert.Equal(t, tt.want, spec.visibleTo(tt.userPath))
		})
	}
}

func TestUserPathAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		userPath string
		allowed  []string
		want     bool
	}{
		{name: "empty allow list admits everyone", userPath: "/x", want: true},
		{name: "root scope admits everyone", userPath: "/x", allowed: []string{"/"}, want: true},
		{name: "exact match", userPath: "/team-a", allowed: []string{"/team-a"}, want: true},
		{name: "subtree match", userPath: "/team-a/dev", allowed: []string{"/team-a"}, want: true},
		{name: "sibling rejected", userPath: "/team-b", allowed: []string{"/team-a"}, want: false},
		{name: "empty user path rejected by scoped list", userPath: "", allowed: []string{"/team-a"}, want: false},
		{name: "prefix is not subtree", userPath: "/team-ab", allowed: []string{"/team-a"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			allowed := normalizeUserPaths(tt.allowed)
			got := userPathAllowed(tt.userPath, allowed)
			require.Equal(t, tt.want, got, "userPathAllowed(%q, %v) = %v, want %v", tt.userPath, tt.allowed, got, tt.want)
		})
	}
}
