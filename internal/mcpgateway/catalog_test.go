package mcpgateway

import (
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResolveNamespacedName(t *testing.T) {
	t.Parallel()
	servers := []string{"github", "git", "git_hub", "beta-tools"}

	tests := []struct {
		name       string
		full       string
		wantServer string
		wantTool   string
		wantOK     bool
	}{
		{name: "simple", full: "github_create_issue", wantServer: "github", wantTool: "create_issue", wantOK: true},
		{name: "longest prefix wins over shorter server", full: "git_hub_sync", wantServer: "git_hub", wantTool: "sync", wantOK: true},
		{name: "shorter server still resolves", full: "git_clone", wantServer: "git", wantTool: "clone", wantOK: true},
		{name: "hyphenated server", full: "beta-tools_search", wantServer: "beta-tools", wantTool: "search", wantOK: true},
		{name: "unknown server", full: "ghost_tool", wantOK: false},
		{name: "no separator", full: "github", wantOK: false},
		{name: "empty tool part", full: "github_", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, tool, ok := ResolveNamespacedName(tt.full, servers)
			if ok != tt.wantOK {
				t.Fatalf("ResolveNamespacedName(%q) ok = %v, want %v", tt.full, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if server != tt.wantServer || tool != tt.wantTool {
				t.Fatalf("ResolveNamespacedName(%q) = (%q, %q), want (%q, %q)", tt.full, server, tool, tt.wantServer, tt.wantTool)
			}
		})
	}
}

func TestNamespacedNameRoundTrip(t *testing.T) {
	t.Parallel()
	full := NamespacedName("github", "create_issue")
	if full != "github_create_issue" {
		t.Fatalf("NamespacedName() = %q, want github_create_issue", full)
	}
	server, tool, ok := ResolveNamespacedName(full, []string{"github"})
	if !ok || server != "github" || tool != "create_issue" {
		t.Fatalf("round trip = (%q, %q, %v)", server, tool, ok)
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
			filtered := filterTools(tools, tt.allowed, tt.disallowed)
			names := make([]string, 0, len(filtered))
			for _, tool := range filtered {
				names = append(names, tool.Name)
			}
			if !slices.Equal(names, tt.want) {
				t.Fatalf("filterTools() = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestFilterToolsNormalizesSchemasForDownstream(t *testing.T) {
	t.Parallel()
	validInput := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}
	tools := []*mcp.Tool{
		{Name: "missing"},
		{Name: "invalid", InputSchema: map[string]any{"type": "string"}, OutputSchema: []string{"not", "a", "schema"}},
		{Name: "valid", InputSchema: validInput, OutputSchema: map[string]any{"type": "object"}},
	}

	filtered := filterTools(tools, nil, nil)
	byName := make(map[string]*mcp.Tool, len(filtered))
	for _, tool := range filtered {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"missing", "invalid", "valid"} {
		if !isObjectSchema(byName[name].InputSchema) {
			t.Fatalf("%s input schema = %#v, want object schema", name, byName[name].InputSchema)
		}
	}
	if byName["invalid"].OutputSchema != nil {
		t.Fatalf("invalid output schema = %#v, want nil", byName["invalid"].OutputSchema)
	}
	if !reflect.DeepEqual(byName["valid"].InputSchema, validInput) {
		t.Fatalf("valid input schema = %#v, want %#v", byName["valid"].InputSchema, validInput)
	}
	if !isObjectSchema(byName["valid"].OutputSchema) {
		t.Fatalf("valid output schema = %#v, want preserved object", byName["valid"].OutputSchema)
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
			if got := userPathAllowed(tt.userPath, allowed); got != tt.want {
				t.Fatalf("userPathAllowed(%q, %v) = %v, want %v", tt.userPath, tt.allowed, got, tt.want)
			}
		})
	}
}
