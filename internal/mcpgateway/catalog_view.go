package mcpgateway

import "github.com/modelcontextprotocol/go-sdk/mcp"

// CatalogFeature is one listed tool or prompt in a catalog view, using the
// upstream's original (un-prefixed) name. ReadOnly and Destructive relay the
// upstream's tool annotations only when it set them explicitly; they are
// hints for operators choosing tools to exclude, not guarantees.
type CatalogFeature struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ReadOnly    bool   `json:"read_only,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

// CatalogResource is one listed resource in a catalog view.
type CatalogResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// CatalogTemplate is one listed resource template in a catalog view.
type CatalogTemplate struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// CatalogView is the admin-facing snapshot of what one upstream currently
// exposes through the gateway. Tools are the ones the operator tool filters
// expose; ExcludedTools are discovered upstream but hidden by those filters.
type CatalogView struct {
	Server        string            `json:"server"`
	Status        ServerStatus      `json:"status"`
	Instructions  string            `json:"instructions,omitempty"`
	Tools         []CatalogFeature  `json:"tools"`
	ExcludedTools []CatalogFeature  `json:"excluded_tools"`
	Prompts       []CatalogFeature  `json:"prompts"`
	Resources     []CatalogResource `json:"resources"`
	Templates     []CatalogTemplate `json:"templates"`
}

// Catalog returns the current catalog snapshot for one server, for the admin
// API and dashboard inspector. ok is false for unknown server names; a known
// server that has never listed successfully returns empty (non-nil) lists.
func (s *Service) Catalog(name string) (CatalogView, bool) {
	u, ok := s.manager.get(name)
	if !ok {
		return CatalogView{}, false
	}
	snapshot, status := u.snapshot()
	view := CatalogView{
		Server:        name,
		Status:        status,
		Tools:         []CatalogFeature{},
		ExcludedTools: []CatalogFeature{},
		Prompts:       []CatalogFeature{},
		Resources:     []CatalogResource{},
		Templates:     []CatalogTemplate{},
	}
	if snapshot == nil {
		return view, true
	}
	view.Instructions = snapshot.instructions
	exposed := make(map[string]struct{}, len(snapshot.tools))
	for _, tool := range snapshot.tools {
		exposed[tool.Name] = struct{}{}
	}
	for _, tool := range snapshot.discovered {
		feature := catalogTool(tool)
		if _, ok := exposed[tool.Name]; ok {
			view.Tools = append(view.Tools, feature)
		} else {
			view.ExcludedTools = append(view.ExcludedTools, feature)
		}
	}
	for _, prompt := range snapshot.prompts {
		view.Prompts = append(view.Prompts, CatalogFeature{Name: prompt.Name, Description: prompt.Description})
	}
	for _, resource := range snapshot.resources {
		view.Resources = append(view.Resources, CatalogResource{URI: resource.URI, Name: resource.Name, Description: resource.Description})
	}
	for _, template := range snapshot.templates {
		view.Templates = append(view.Templates, CatalogTemplate{URITemplate: template.URITemplate, Name: template.Name, Description: template.Description})
	}
	return view, true
}

// catalogTool prefers the human-facing description, falling back to the
// annotation title so the inspector never shows a blank row for tools that
// only set display metadata.
func catalogTool(tool *mcp.Tool) CatalogFeature {
	feature := CatalogFeature{Name: tool.Name, Description: tool.Description}
	if feature.Description == "" && tool.Title != "" {
		feature.Description = tool.Title
	}
	annotations := tool.Annotations
	if annotations == nil {
		return feature
	}
	if feature.Description == "" && annotations.Title != "" {
		feature.Description = annotations.Title
	}
	// The MCP spec defaults destructiveHint to true for non-read-only tools;
	// only an explicit hint is shown, so unannotated tools stay unbadged.
	feature.ReadOnly = annotations.ReadOnlyHint
	feature.Destructive = !annotations.ReadOnlyHint && annotations.DestructiveHint != nil && *annotations.DestructiveHint
	return feature
}
