package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidVirtualServer marks a virtual server the admin API refuses:
// invalid fields, unknown or nested members, or a name a server holds.
var ErrInvalidVirtualServer = errors.New("invalid virtual MCP server")

type invalidVirtualServerError struct{ msg string }

func (e invalidVirtualServerError) Error() string { return e.msg }

func (e invalidVirtualServerError) Is(target error) bool { return target == ErrInvalidVirtualServer }

func invalidVirtual(format string, args ...any) error {
	return invalidVirtualServerError{msg: fmt.Sprintf(format, args...)}
}

// UpsertVirtual validates and persists one admin-managed virtual server, then
// reconciles. Members must be existing servers. A new virtual server may not
// take a server's slug; one stored before the server appeared stays editable,
// and its edit fails rather than recreating it if a delete wins the race.
// It returns the saved definition's admin view.
func (s *Service) UpsertVirtual(ctx context.Context, virtual ManagedVirtualServer) (VirtualServerView, error) {
	if s.virtualStore == nil {
		return VirtualServerView{}, fmt.Errorf("mcp virtual server persistence is unavailable")
	}
	if err := virtual.Validate(); err != nil {
		return VirtualServerView{}, invalidVirtual("%s", err.Error())
	}
	if s.IsManagedVirtual(virtual.Name) {
		return VirtualServerView{}, invalidVirtual("virtual MCP server %q is managed by config/env and is read-only", virtual.Name)
	}
	for _, member := range virtual.Servers {
		if s.IsVirtual(member) {
			return VirtualServerView{}, invalidVirtual("member %q is a virtual server; virtual servers can only include MCP servers", member)
		}
		if _, ok := s.manager.get(member); !ok {
			return VirtualServerView{}, invalidVirtual("member %q matches no MCP server", member)
		}
	}

	write := s.virtualStore.UpsertVirtual
	if _, taken := s.manager.get(virtual.Name); taken {
		write = s.virtualStore.UpdateVirtual
	}
	if err := write(ctx, virtual); err != nil {
		if errors.Is(err, ErrNotFound) {
			return VirtualServerView{}, invalidVirtual("name %q is used by MCP server %q; choose another name", virtual.Name, virtual.Name)
		}
		return VirtualServerView{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return VirtualServerView{}, fmt.Errorf("mcp virtual server %q was saved but not applied: %w", virtual.Name, err)
	}
	return s.virtualView(virtual.Spec()), nil
}

// DeleteVirtual removes one admin-managed virtual server, then reconciles.
func (s *Service) DeleteVirtual(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if s.virtualStore == nil {
		return fmt.Errorf("mcp virtual server persistence is unavailable")
	}
	if s.IsManagedVirtual(name) {
		return invalidVirtual("virtual MCP server %q is managed by config/env and is read-only", name)
	}
	if err := s.virtualStore.DeleteVirtual(ctx, name); err != nil {
		return err
	}
	if err := s.Reload(ctx); err != nil {
		return fmt.Errorf("mcp virtual server %q was deleted but the running set was not updated: %w", name, err)
	}
	s.closeEndpointSessions(virtualBindingPrefix + name)
	return nil
}
