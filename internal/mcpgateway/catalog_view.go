package mcpgateway

import "github.com/modelcontextprotocol/go-sdk/mcp"

// CatalogFeature is one listed tool or prompt in a catalog view, using the
// upstream's original (un-prefixed) name.
type CatalogFeature struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
// exposes through the gateway, after the operator tool filters were applied.
type CatalogView struct {
	Server       string            `json:"server"`
	Status       ServerStatus      `json:"status"`
	Instructions string            `json:"instructions,omitempty"`
	Tools        []CatalogFeature  `json:"tools"`
	Prompts      []CatalogFeature  `json:"prompts"`
	Resources    []CatalogResource `json:"resources"`
	Templates    []CatalogTemplate `json:"templates"`
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
		Server:    name,
		Status:    status,
		Tools:     []CatalogFeature{},
		Prompts:   []CatalogFeature{},
		Resources: []CatalogResource{},
		Templates: []CatalogTemplate{},
	}
	if snapshot == nil {
		return view, true
	}
	view.Instructions = snapshot.instructions
	for _, tool := range snapshot.tools {
		view.Tools = append(view.Tools, catalogFeature(tool.Name, tool.Description, tool.Annotations, tool.Title))
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

// catalogFeature prefers the human-facing description, falling back to the
// annotation title so the inspector never shows a blank row for tools that
// only set display metadata.
func catalogFeature(name, description string, annotations *mcp.ToolAnnotations, title string) CatalogFeature {
	feature := CatalogFeature{Name: name, Description: description}
	if feature.Description == "" && title != "" {
		feature.Description = title
	}
	if feature.Description == "" && annotations != nil && annotations.Title != "" {
		feature.Description = annotations.Title
	}
	return feature
}
