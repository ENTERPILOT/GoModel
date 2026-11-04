package mcpgateway

import (
	"context"
	"crypto/rand"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/config"
)

// VirtualStore persists admin-managed virtual servers. The SQL and MongoDB
// stores implement it next to Store.
type VirtualStore interface {
	ListVirtual(ctx context.Context) ([]ManagedVirtualServer, error)
	GetVirtual(ctx context.Context, name string) (*ManagedVirtualServer, error)
	UpsertVirtual(ctx context.Context, virtual ManagedVirtualServer) error
	// UpdateVirtual rewrites an existing row and returns ErrNotFound when the
	// row is gone, so an edit racing a delete cannot recreate it.
	UpdateVirtual(ctx context.Context, virtual ManagedVirtualServer) error
	// DeleteVirtual removes a row and returns it as it was deleted, in the
	// same operation, so a caller learns which incarnation it removed even
	// when another instance replaced the row just before.
	DeleteVirtual(ctx context.Context, name string) (*ManagedVirtualServer, error)
}

// ManagedVirtualServer is one admin-managed virtual server row.
type ManagedVirtualServer struct {
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Servers       []string  `json:"servers"`
	ToolDiscovery string    `json:"tool_discovery,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Generation is a random token the store sets when it creates the row
	// and keeps across edits, so it tells one creation of a name from the
	// next. Rows created before it have none; see incarnation.
	Generation string `json:"-"`
}

// Validate applies the declarative-config rules and normalizes the row in
// place. Member existence and name clashes depend on the running gateway, so
// Service.UpsertVirtual checks them.
func (v *ManagedVirtualServer) Validate() error {
	name := strings.ToLower(strings.TrimSpace(v.Name))
	if err := config.ValidateMCPVirtualServerName(name); err != nil {
		return err
	}
	cfg := v.config()
	if err := config.NormalizeMCPVirtualServer(&cfg); err != nil {
		return err
	}
	v.Name = name
	v.Description = cfg.Description
	v.Servers = cfg.Servers
	v.ToolDiscovery = cfg.ToolDiscovery
	return nil
}

func (v ManagedVirtualServer) config() config.MCPVirtualServerConfig {
	return config.MCPVirtualServerConfig{
		Description:   v.Description,
		Servers:       slices.Clone(v.Servers),
		ToolDiscovery: v.ToolDiscovery,
	}
}

// Spec converts the row into a runtime spec.
func (v ManagedVirtualServer) Spec() VirtualServerSpec {
	spec := VirtualFromConfig(v.Name, v.config())
	spec.Managed = false
	spec.Incarnation = v.incarnation()
	return spec
}

// incarnation identifies this creation of the row: its Generation, or for a
// row created before generations its creation time. Those times are whole
// seconds in SQL, but every row created since has a Generation, so two
// incarnations of one name never share one.
func (v ManagedVirtualServer) incarnation() string {
	if v.Generation != "" {
		return v.Generation
	}
	if v.CreatedAt.IsZero() {
		return ""
	}
	return "created-" + strconv.FormatInt(v.CreatedAt.UnixNano(), 10)
}

// stampVirtualUpsert sets timestamps, and the generation an insert stores;
// an update keeps the row's own.
func stampVirtualUpsert(virtual *ManagedVirtualServer) {
	now := time.Now().UTC()
	if virtual.CreatedAt.IsZero() {
		virtual.CreatedAt = now
	}
	virtual.UpdatedAt = now
	if virtual.Generation == "" {
		virtual.Generation = rand.Text()
	}
}
